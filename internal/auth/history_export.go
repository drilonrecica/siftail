package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/logs"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

const historyExportConfirmation = "export matching history"

func (b *Browser) historyExport(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, err := logs.ParseHistoryQuery(cloneURLValues(r.URL.Query()), b.now())
	if err != nil {
		b.rejectHistoryExport(w, r, session.CSRFToken, logs.HistoryQuery{},
			"invalid_query", http.StatusBadRequest,
			"Check the export time range and filters.")
		return
	}
	format, confirmed, err := parseHistoryExportForm(r)
	if err != nil {
		b.rejectHistoryExport(w, r, session.CSRFToken, query,
			"invalid_request", http.StatusBadRequest,
			"Check the export format and confirmation.")
		return
	}
	if !confirmed {
		b.rejectHistoryExport(w, r, session.CSRFToken, query,
			"confirmation_required", http.StatusUnprocessableEntity,
			"Confirm that the complete matching History range will leave Siftail.")
		return
	}
	if b.exports == nil || b.audit == nil || b.exportDataDir == "" {
		b.failHistoryExport(w, r, session.CSRFToken, query, format,
			"unavailable")
		return
	}
	select {
	case b.exportSlot <- struct{}{}:
		defer func() { <-b.exportSlot }()
	default:
		b.rejectHistoryExport(w, r, session.CSRFToken, query,
			"busy", http.StatusConflict,
			"Another History export is already in progress.")
		return
	}

	artifact, err := os.CreateTemp(b.exportDataDir, ".siftail-export-*")
	if err != nil {
		b.failHistoryExport(w, r, session.CSRFToken, query, format,
			"staging_failed")
		return
	}
	artifactPath := artifact.Name()
	defer func() {
		_ = artifact.Close()
		_ = os.Remove(artifactPath)
	}()
	if err := artifact.Chmod(0600); err != nil {
		b.failHistoryExport(w, r, session.CSRFToken, query, format,
			"staging_failed")
		return
	}
	result, err := b.exports.Export(r.Context(), query, format, artifact)
	if err == nil {
		err = artifact.Sync()
	}
	if err == nil {
		err = r.Context().Err()
	}
	if err == nil {
		_, err = artifact.Seek(0, io.SeekStart)
	}
	if err != nil {
		category := logs.ExportFailureCategory(err)
		outcome := audit.OutcomeFailed
		status := http.StatusServiceUnavailable
		message := "The export could not be completed. No partial file was sent."
		switch category {
		case "canceled":
			outcome = audit.OutcomeCanceled
		case "row_limit", "byte_limit":
			outcome = audit.OutcomeRejected
			status = http.StatusUnprocessableEntity
			message = "The export exceeds a hard limit. Narrow the filters or time range."
		}
		if auditErr := b.recordHistoryExport(
			r.Context(), query, format, outcome, category, result.Rows,
		); auditErr != nil {
			b.renderHistoryExportError(w, r, session.CSRFToken, query,
				http.StatusServiceUnavailable,
				"The export audit could not be recorded. No file was sent.")
			return
		}
		if !errors.Is(err, context.Canceled) {
			b.renderHistoryExportError(w, r, session.CSRFToken, query, status, message)
		}
		return
	}
	if err := b.recordHistoryExport(
		r.Context(), query, format, audit.OutcomeSucceeded, "complete", result.Rows,
	); err != nil {
		b.renderHistoryExportError(w, r, session.CSRFToken, query,
			http.StatusServiceUnavailable,
			"The export audit could not be recorded. No file was sent.")
		return
	}

	name := historyExportFilename(query, format)
	w.Header().Set("Content-Type", historyExportContentType(format))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", strconv.FormatInt(result.Bytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.CopyN(w, artifact, result.Bytes); err != nil {
		_, _ = b.audit.Record(context.WithoutCancel(r.Context()),
			historyExportAuditInput(
				context.WithoutCancel(r.Context()), query, format,
				audit.OutcomeCanceled, "delivery_interrupted", result.Rows,
			))
	}
}

func parseHistoryExportForm(r *http.Request) (string, bool, error) {
	for key, values := range r.PostForm {
		if key != "csrf_token" && key != "format" && key != "confirmation" {
			return "", false, errors.New("unknown export form field")
		}
		if len(values) != 1 {
			return "", false, errors.New("export form field must occur once")
		}
	}
	if len(r.PostForm) != 3 || len(r.PostForm["csrf_token"]) != 1 ||
		len(r.PostForm["format"]) != 1 || len(r.PostForm["confirmation"]) != 1 {
		return "", false, errors.New("export form is incomplete")
	}
	format := r.PostForm.Get("format")
	if format != logs.ExportFormatText && format != logs.ExportFormatNDJSON {
		return "", false, errors.New("unsupported export format")
	}
	return format, r.PostForm.Get("confirmation") == historyExportConfirmation, nil
}

func (b *Browser) rejectHistoryExport(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	query logs.HistoryQuery,
	category string,
	status int,
	message string,
) {
	format := r.PostForm.Get("format")
	if format != logs.ExportFormatText && format != logs.ExportFormatNDJSON {
		format = logs.ExportFormatText
	}
	if b.audit == nil || b.recordHistoryExport(
		r.Context(), query, format, audit.OutcomeRejected, category, 0,
	) != nil {
		status = http.StatusServiceUnavailable
		message = "The export audit could not be recorded. No file was sent."
	}
	b.renderHistoryExportError(w, r, csrfToken, query, status, message)
}

func (b *Browser) failHistoryExport(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	query logs.HistoryQuery,
	format, category string,
) {
	if format != logs.ExportFormatText && format != logs.ExportFormatNDJSON {
		format = logs.ExportFormatText
	}
	message := "The export could not be completed. No partial file was sent."
	if b.audit == nil || b.recordHistoryExport(
		r.Context(), query, format, audit.OutcomeFailed, category, 0,
	) != nil {
		message = "The export audit could not be recorded. No file was sent."
	}
	b.renderHistoryExportError(w, r, csrfToken, query,
		http.StatusServiceUnavailable, message)
}

func (b *Browser) recordHistoryExport(
	ctx context.Context,
	query logs.HistoryQuery,
	format string,
	outcome audit.Outcome,
	category string,
	rows int,
) error {
	_, err := b.audit.Record(context.WithoutCancel(ctx), historyExportAuditInput(
		context.WithoutCancel(ctx), query, format, outcome, category, rows,
	))
	return err
}

func historyExportAuditInput(
	ctx context.Context,
	query logs.HistoryQuery,
	format string,
	outcome audit.Outcome,
	category string,
	rows int,
) audit.Input {
	metadata := audit.Metadata{
		audit.MetadataExportFormat:   format,
		audit.MetadataResultCategory: category,
	}
	if rows > 0 || outcome == audit.OutcomeSucceeded {
		metadata[audit.MetadataAffectedCount] = strconv.Itoa(rows)
	}
	input := audit.InputFromContext(
		ctx, audit.CategoryExport, "export.history", outcome, metadata,
	)
	input.OccurredAt = time.Now().UTC()
	if query.ServerID > 0 {
		serverID := query.ServerID
		input.ServerID = &serverID
	}
	return input
}

func (b *Browser) renderHistoryExportError(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	query logs.HistoryQuery,
	status int,
	message string,
) {
	if query.FromUS > 0 && query.ToUS > query.FromUS {
		view, err := b.historyView(r, query, false)
		if err == nil {
			view.ExportError = message
			if err := b.ui.Shell(w, status, webui.ShellView{
				CSRFToken: csrfToken, Mode: "history", History: view,
			}); err == nil {
				return
			}
		}
	}
	http.Error(w, message, status)
}

func historyExportFilename(query logs.HistoryQuery, format string) string {
	from := time.UnixMicro(query.FromUS).UTC().Format("20060102T150405Z")
	to := time.UnixMicro(query.ToUS).UTC().Format("20060102T150405Z")
	extension := "txt"
	if format == logs.ExportFormatNDJSON {
		extension = "ndjson"
	}
	return strings.Join([]string{"siftail-history", from, to}, "-") + "." + extension
}

func historyExportContentType(format string) string {
	if format == logs.ExportFormatNDJSON {
		return "application/x-ndjson"
	}
	return "text/plain; charset=utf-8"
}

func historyExportStagingFiles(dataDir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dataDir, ".siftail-export-*"))
}
