package auth

import (
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"time"

	statusstate "github.com/drilonrecica/siftail/internal/status"
	"github.com/drilonrecica/siftail/internal/version"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) statusPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	if len(r.URL.Query()) != 0 {
		b.renderStatus(w, session.CSRFToken, http.StatusBadRequest, webui.StatusView{
			Severity: "Unavailable", Error: "Check the Status page parameters.",
		})
		return
	}
	if b.status == nil {
		b.renderStatus(w, session.CSRFToken, http.StatusServiceUnavailable, webui.StatusView{
			Severity: "Unavailable", Error: "Status is temporarily unavailable.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	snapshot, err := b.status.Read(r.Context())
	if err != nil {
		b.renderStatus(w, session.CSRFToken, http.StatusServiceUnavailable, webui.StatusView{
			Severity:       "Unavailable",
			Error:          "Status could not be read because local storage is temporarily unavailable.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	b.renderStatus(w, session.CSRFToken, http.StatusOK, statusView(snapshot, b.now()))
}

func (b *Browser) renderStatus(
	w http.ResponseWriter,
	csrfToken string,
	httpStatus int,
	view webui.StatusView,
) {
	if err := b.ui.Shell(w, httpStatus, webui.ShellView{
		CSRFToken: csrfToken, Mode: "status", Status: view,
	}); err != nil {
		http.Error(w, "Status is temporarily unavailable.", http.StatusInternalServerError)
	}
}

func statusView(snapshot statusstate.OperationalSnapshot, now time.Time) webui.StatusView {
	state := snapshot.State
	footprint := snapshot.DatabaseBytes + snapshot.WALBytes + snapshot.SHMBytes
	usage := 0
	if snapshot.Retention.MaxDatabaseBytes > 0 {
		usage = int(footprint * 100 / snapshot.Retention.MaxDatabaseBytes)
	}
	severity := "Healthy"
	if state.Degraded || !state.DatabaseWritable {
		severity = "Degraded"
	} else if !state.WriterReady {
		severity = "Unavailable"
	} else if usage >= 90 {
		severity = "Attention"
	}
	progressUsage := usage
	if progressUsage > 100 {
		progressUsage = 100
	}
	view := webui.StatusView{
		Severity:          severity,
		Version:           version.Version + " · " + version.Commit,
		Uptime:            formatUptime(now.Sub(state.StartedAt)),
		Architecture:      runtime.GOOS + "/" + runtime.GOARCH,
		UIReady:           "ready",
		IngestionReady:    readinessLabel(state),
		DatabaseSize:      formatStatusBytes(snapshot.DatabaseBytes),
		WALSize:           formatStatusBytes(snapshot.WALBytes),
		SHMSize:           formatStatusBytes(snapshot.SHMBytes),
		DatabaseLimit:     formatStatusBytes(snapshot.Retention.MaxDatabaseBytes),
		DatabaseUsage:     progressUsage,
		OldestEvent:       formatOptionalStatusTime(snapshot.OldestEventUS),
		NewestEvent:       formatOptionalStatusTime(snapshot.NewestEventUS),
		RetentionAge:      strconv.Itoa(snapshot.Retention.AgeDays) + " days",
		LastCleanup:       "Not yet run",
		LastCleanupResult: "No cleanup result",
		EventsToday:       strconv.FormatInt(snapshot.EventsToday, 10),
		RecentRate:        fmt.Sprintf("%d events/min (last 60 seconds)", state.RecentEvents),
		QueuedEvents:      strconv.FormatInt(snapshot.Queue.QueuedEvents, 10),
		QueuedBytes:       formatStatusBytes(snapshot.Queue.QueuedBytes),
		RejectedBatches:   strconv.FormatUint(state.RejectedBatches, 10),
		AcceptedEvents:    strconv.FormatUint(state.AcceptedEvents, 10),
		LastIngest:        "Never",
		LastDatabaseError: "None",
		Diagnostics:       make([]webui.StatusDiagnosticView, len(state.Diagnostics)),
	}
	if state.LastCleanup != nil {
		view.LastCleanup = formatStatusTime(*state.LastCleanup)
		result := state.LastCleanupResult
		view.LastCleanupResult = fmt.Sprintf(
			"%d age-deleted · %d size-deleted",
			result.AgeDeleted, result.SizeDeleted,
		)
		if result.CheckpointBusy {
			view.LastCleanupResult += " · checkpoint busy"
		}
	}
	if state.LastSuccessfulIngest != nil {
		view.LastIngest = formatStatusTime(*state.LastSuccessfulIngest)
	}
	if state.LastDatabaseError != nil {
		view.LastDatabaseError = state.LastDatabaseError.Category + " · " +
			formatStatusTime(state.LastDatabaseError.At)
	}
	for index, diagnostic := range state.Diagnostics {
		view.Diagnostics[index] = webui.StatusDiagnosticView{
			Time:     formatStatusTime(diagnostic.At),
			Category: diagnostic.Category,
			Summary:  diagnostic.Summary,
		}
	}
	return view
}

func readinessLabel(state statusstate.Snapshot) string {
	if state.WriterReady && state.DatabaseWritable && !state.Degraded {
		return "ready"
	}
	return "not ready"
}

func formatStatusBytes(bytes int64) string {
	if bytes < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/(1<<10))
	}
	if bytes < 1<<30 {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/(1<<20))
	}
	return fmt.Sprintf("%.2f GiB", float64(bytes)/(1<<30))
}

func formatOptionalStatusTime(value *int64) string {
	if value == nil {
		return "No retained events"
	}
	return formatStatusTime(time.UnixMicro(*value))
}

func formatStatusTime(value time.Time) string {
	return value.UTC().Format("02 Jan 2006 15:04:05 UTC")
}

func formatUptime(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Truncate(time.Second)
	days := duration / (24 * time.Hour)
	duration %= 24 * time.Hour
	if days > 0 {
		return fmt.Sprintf("%dd %s", days, duration)
	}
	return duration.String()
}
