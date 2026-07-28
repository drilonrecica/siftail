package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/backup"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) backupPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	view := b.backupView(session.CSRFToken)
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{
		CSRFToken: session.CSRFToken, Mode: "backup", Backup: view,
	}); err != nil {
		http.Error(w, "Backup is temporarily unavailable.",
			http.StatusInternalServerError)
	}
}

func (b *Browser) backupRegion(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	if err := b.ui.Backup(w, http.StatusOK, b.backupView(session.CSRFToken)); err != nil {
		http.Error(w, "Backup is temporarily unavailable.",
			http.StatusInternalServerError)
	}
}

func (b *Browser) backupStart(w http.ResponseWriter, r *http.Request) {
	b.startBackup(w, r, backup.TypeFull)
}

func (b *Browser) configurationBackupStart(
	w http.ResponseWriter,
	r *http.Request,
) {
	b.startBackup(w, r, backup.TypeConfiguration)
}

func (b *Browser) startBackup(
	w http.ResponseWriter,
	r *http.Request,
	backupType string,
) {
	session, _ := BrowserSessionFromContext(r.Context())
	if b.backups == nil {
		b.renderBackupError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Backup is temporarily unavailable.")
		return
	}
	outputPath := r.PostForm.Get("output_path")
	if len(r.PostForm) != 2 || len(r.PostForm["csrf_token"]) != 1 ||
		len(r.PostForm["output_path"]) != 1 || !validBackupPathInput(outputPath) {
		b.renderBackupError(w, session.CSRFToken, http.StatusBadRequest,
			"Enter a valid server-side output path.")
		return
	}
	start := b.backups.Start
	if backupType == backup.TypeConfiguration {
		start = b.backups.StartConfiguration
	}
	if _, err := start(r.Context(), outputPath); err != nil {
		if errors.Is(err, backup.ErrBackupInProgress) {
			b.renderBackupError(w, session.CSRFToken, http.StatusConflict,
				"A backup operation is already in progress.")
			return
		}
		b.renderBackupError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Backup could not be started.")
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

func (b *Browser) backupVerifyStart(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	if b.backups == nil {
		b.renderBackupError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Backup verification is temporarily unavailable.")
		return
	}
	artifactPath := r.PostForm.Get("artifact_path")
	if len(r.PostForm) != 2 || len(r.PostForm["csrf_token"]) != 1 ||
		len(r.PostForm["artifact_path"]) != 1 ||
		!validBackupPathInput(artifactPath) {
		b.renderBackupError(w, session.CSRFToken, http.StatusBadRequest,
			"Enter a valid server-side artifact path.")
		return
	}
	if _, err := b.backups.StartVerify(r.Context(), artifactPath); err != nil {
		if errors.Is(err, backup.ErrBackupInProgress) {
			b.renderBackupError(w, session.CSRFToken, http.StatusConflict,
				"A backup operation is already in progress.")
			return
		}
		b.renderBackupError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Backup verification could not be started.")
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

func (b *Browser) renderBackupError(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	message string,
) {
	view := b.backupView(csrfToken)
	view.Error = message
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken, Mode: "backup", Backup: view,
	}); err != nil {
		http.Error(w, "Backup is temporarily unavailable.",
			http.StatusInternalServerError)
	}
}

func (b *Browser) backupView(csrfToken string) webui.BackupView {
	view := webui.BackupView{
		CSRFToken: csrfToken, State: backup.StateIdle,
		StateLabel: "No backup has run during this process.",
	}
	if b.backups == nil {
		view.Error = "Backup is temporarily unavailable."
		return view
	}
	status := b.backups.Snapshot()
	view.State = status.State
	view.Operation = status.Operation
	view.BackupType = status.BackupType
	view.TotalUnits = strconv.Itoa(status.TotalUnits)
	view.CompletedUnits = strconv.Itoa(status.CompletedUnits)
	view.Unit = status.Unit
	if status.StartedAt != nil {
		view.StartedAt = status.StartedAt.UTC().Format(time.RFC3339)
	}
	if status.CompletedAt != nil {
		view.CompletedAt = status.CompletedAt.UTC().Format(time.RFC3339)
	}
	switch status.State {
	case backup.StateRunning:
		view.StateLabel = "Full backup in progress."
		if status.BackupType == backup.TypeConfiguration {
			view.StateLabel = "Configuration-only backup in progress."
		} else if status.Operation == backup.OperationVerify {
			view.StateLabel = "Backup verification in progress."
		}
	case backup.StateSucceeded:
		view.StateLabel = "Backup verified."
		if status.Result != nil {
			view.BackupType = status.Result.Type
			view.Name = status.Result.Name
			view.Size = formatBackupBytes(status.Result.Bytes)
			view.Checksum = status.Result.SHA256
		}
	case backup.StateCanceled:
		view.StateLabel = "Backup was canceled before a verified artifact was published."
	case backup.StateFailed:
		view.StateLabel = "Backup did not complete. Check destination capacity and permissions."
		if status.Operation == backup.OperationVerify {
			view.StateLabel = "Backup verification failed. Check the artifact type, compatibility, integrity, and permissions."
		}
	}
	return view
}

func validBackupPathInput(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func formatBackupBytes(value int64) string {
	if value < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(value)/(1<<10))
	}
	if value < 1<<30 {
		return fmt.Sprintf("%.1f MiB", float64(value)/(1<<20))
	}
	return fmt.Sprintf("%.2f GiB", float64(value)/(1<<30))
}
