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
	"syscall"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/logs"
	statusstate "github.com/drilonrecica/siftail/internal/status"
	"github.com/drilonrecica/siftail/internal/web"
)

const historyExportConfirmation = "export matching history"
const historyExportStagingPrefix = ".siftail-export-"

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

	limits := b.exports.Limits()
	controller := http.NewResponseController(w)
	// The server-wide 30-second WriteTimeout starts when the request is read.
	// Staging may legitimately run for the store's full bounded duration, so
	// the deadline covers it plus one response write; delivery then extends
	// it per chunk. A writer without deadline support
	// (tests, wrappers) simply keeps its own behavior.
	_ = controller.SetWriteDeadline(
		b.now().Add(limits.Timeout + b.exportWriteTimeout),
	)
	if err := b.checkHistoryExportSpace(limits.MaxBytes); err != nil {
		b.failHistoryExportStatus(w, r, session.CSRFToken, query, format,
			"insufficient_space", http.StatusInsufficientStorage,
			"The data volume does not have enough free space to stage this export. No file was sent.")
		return
	}

	artifact, err := os.CreateTemp(
		b.exportDataDir, historyExportStagingPrefix+"*",
	)
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
	_ = controller.SetWriteDeadline(b.now().Add(b.exportWriteTimeout))
	if err != nil {
		category := logs.ExportFailureCategory(err)
		outcome := audit.OutcomeFailed
		status := http.StatusServiceUnavailable
		message := "The export could not be completed. No partial file was sent."
		switch category {
		case "canceled":
			outcome = audit.OutcomeCanceled
		case "timeout":
			message = "The export took longer than its time limit. Narrow the filters or time range. No partial file was sent."
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
	// The mandatory success audit commits before any header leaves
	// (DOMAIN.md export rules). Every failure after this point appends the
	// separate canceled delivery_interrupted record, so a truncated response
	// is never left with only the success row.
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
	if err := b.deliverHistoryExport(w, controller, artifact, result.Bytes); err != nil {
		b.recordHistoryExportInterrupted(r, query, format, result.Rows)
	}
}

// historyExportChunkBytes is the delivery unit. Each chunk gets a fresh write
// deadline, so a large artifact on a slow link completes while a stalled
// client still fails within one deadline.
const historyExportChunkBytes = 64 << 10

// historyExportSpaceReserve is kept free on the data volume beyond the
// largest possible artifact, so a maximal export never consumes the final
// space SQLite needs for ingestion commits and WAL growth.
const historyExportSpaceReserve = 64 << 20

func (b *Browser) checkHistoryExportSpace(maximum int64) error {
	available, err := b.exportSpace(b.exportDataDir)
	if err != nil {
		return errors.New("inspect export staging capacity")
	}
	if maximum < 0 || available < uint64(maximum)+historyExportSpaceReserve {
		return errors.New("export staging has insufficient space")
	}
	return nil
}

func filesystemAvailableBytes(directory string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}

// deliverHistoryExport copies exactly size bytes in bounded chunks, extending
// the write deadline before each one, then flushes so a buffered tail that
// cannot reach the client is reported rather than lost at handler return.
func (b *Browser) deliverHistoryExport(
	w http.ResponseWriter,
	controller *http.ResponseController,
	artifact io.Reader,
	size int64,
) error {
	buffer := make([]byte, historyExportChunkBytes)
	remaining := size
	for remaining > 0 {
		chunk := buffer
		if remaining < int64(len(chunk)) {
			chunk = chunk[:remaining]
		}
		if _, err := io.ReadFull(artifact, chunk); err != nil {
			return errors.New("read export artifact")
		}
		_ = controller.SetWriteDeadline(b.now().Add(b.exportWriteTimeout))
		written, err := w.Write(chunk)
		if err != nil || written != len(chunk) {
			return errors.New("write export response")
		}
		remaining -= int64(written)
	}
	_ = controller.SetWriteDeadline(b.now().Add(b.exportWriteTimeout))
	if err := controller.Flush(); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return errors.New("flush export response")
	}
	return nil
}

// recordHistoryExportInterrupted appends the canceled delivery record. A
// failed audit write is retried once; if it still fails, a sanitized Status
// diagnostic tells the operator that the audit trail shows a success for an
// export whose delivery did not complete.
func (b *Browser) recordHistoryExportInterrupted(
	r *http.Request,
	query logs.HistoryQuery,
	format string,
	rows int,
) {
	for attempt := 0; attempt < 2; attempt++ {
		if b.recordHistoryExport(
			r.Context(), query, format, audit.OutcomeCanceled,
			"delivery_interrupted", rows,
		) == nil {
			return
		}
	}
	if b.status != nil {
		_ = b.status.RecordDiagnostic(statusstate.DiagnosticInput{
			At: b.now(), Component: "export",
			Category:  "export_delivery_audit_failed",
			RequestID: web.RequestIDFromContext(r.Context()),
		})
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
	b.failHistoryExportStatus(w, r, csrfToken, query, format, category,
		http.StatusServiceUnavailable,
		"The export could not be completed. No partial file was sent.")
}

func (b *Browser) failHistoryExportStatus(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	query logs.HistoryQuery,
	format, category string,
	status int,
	message string,
) {
	if format != logs.ExportFormatText && format != logs.ExportFormatNDJSON {
		format = logs.ExportFormatText
	}
	if b.audit == nil || b.recordHistoryExport(
		r.Context(), query, format, audit.OutcomeFailed, category, 0,
	) != nil {
		status = http.StatusServiceUnavailable
		message = "The export audit could not be recorded. No file was sent."
	}
	b.renderHistoryExportError(w, r, csrfToken, query, status, message)
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
			shell := b.shellBase(w, r, "history")
			shell.History = view
			if err := b.ui.Shell(w, status, shell); err == nil {
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
	return filepath.Glob(filepath.Join(dataDir, historyExportStagingPrefix+"*"))
}

// CleanupHistoryExportStaging removes private artifacts left by a hard process
// interruption before the database is opened. The reserved pattern is strict,
// directory reads are chunked, and symlinks or broadly readable matches stop
// startup rather than being followed or silently removed.
func CleanupHistoryExportStaging(dataDir string) error {
	if dataDir == "" {
		return errors.New("History export recovery directory is unavailable")
	}
	directory, err := os.Open(dataDir)
	if err != nil {
		return errors.New("open History export recovery directory")
	}
	defer directory.Close()
	removed := false
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if !validHistoryExportStagingName(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() ||
				info.Mode().Perm()&0077 != 0 {
				return errors.New("unsafe History export recovery artifact")
			}
			if err := os.Remove(filepath.Join(dataDir, entry.Name())); err != nil {
				return errors.New("remove History export recovery artifact")
			}
			removed = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return errors.New("read History export recovery directory")
		}
	}
	if removed {
		if err := directory.Sync(); err != nil {
			return errors.New("synchronize History export recovery directory")
		}
	}
	return nil
}

func validHistoryExportStagingName(name string) bool {
	suffix, ok := strings.CutPrefix(name, historyExportStagingPrefix)
	if !ok || len(suffix) < 1 || len(suffix) > 20 {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
