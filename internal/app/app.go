package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/sessions"
	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
	"golang.org/x/sync/errgroup"
)

// App composes the long-running Siftail process.
type App struct {
	cfg          config.Config
	logger       *slog.Logger
	db           *database.DB
	coordinator  *database.Coordinator
	admission    *ingest.Admission
	queue        *ingest.Queue
	ingestHTTP   http.Handler
	browser      *auth.Browser
	shuttingDown atomic.Bool
}

// New creates an application root. It does not open listeners or databases.
func New(cfg config.Config, logger *slog.Logger) *App {
	return &App{cfg: cfg, logger: logger}
}

// Run starts all critical components and blocks until the context is cancelled
// or a critical component fails. The first critical error cancels the
// application context.
func (a *App) Run(ctx context.Context) error {
	g, serverCtx := errgroup.WithContext(ctx)

	if err := a.ensureDataDir(); err != nil {
		return err
	}
	db, err := database.Open(serverCtx, a.cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("database startup: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			a.logger.Error("database shutdown failed", "component", "database", "error_category", "database_close")
		}
	}()
	a.db = db
	defer func() { a.db = nil }()

	controlListener, err := a.openControlSocket()
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer func() {
		_ = controlListener.Close()
		_ = os.Remove(a.controlSocketPath())
	}()

	coordinator := database.NewCoordinator(db.Writer())
	a.coordinator = coordinator
	defer func() { a.coordinator = nil }()
	coordinatorCtx, cancelCoordinator := context.WithCancel(context.Background())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(coordinatorCtx) }()
	<-coordinator.Ready()
	administratorStore := auth.NewCoordinatedStore(db.Reader(), coordinator)
	sessionStore := sessions.NewCoordinatedStore(db.Reader(), coordinator)
	a.browser = auth.NewBrowser(administratorStore, sessionStore, auth.BrowserConfig{
		PublicURL: a.cfg.PublicURL, TrustedProxyCIDRs: a.cfg.TrustedProxyCIDRs,
	})
	defer func() { a.browser = nil }()

	if err := a.initializeIngestion(); err != nil {
		coordinator.Close()
		cancelCoordinator()
		<-coordinatorDone
		return err
	}
	defer func() {
		a.admission = nil
		a.queue = nil
		a.ingestHTTP = nil
	}()
	writerDone := make(chan error, 1)
	writerCtx, cancelWriter := context.WithCancel(context.Background())
	go func() {
		writerDone <- ingest.NewWriterWorker(a.queue, ingest.NewBatchWriter(coordinator, nil)).Run(writerCtx)
	}()

	g.Go(func() error { return a.runControlServer(serverCtx, controlListener) })
	g.Go(func() error { return a.runUIServer(serverCtx) })
	g.Go(func() error { return a.runIngestServer(serverCtx) })
	g.Go(func() error {
		return sessions.NewCleanupWorker(sessionStore, time.Hour, func(error) {
			a.logger.Warn("session cleanup failed",
				"component", "sessions",
				"error_category", "session_cleanup",
			)
		}).Run(serverCtx)
	})

	serverErr := g.Wait()
	a.shuttingDown.Store(true)
	a.admission.Close()
	a.queue.Close()
	var writerErr error
	select {
	case writerErr = <-writerDone:
	case <-time.After(a.cfg.ShutdownTimeout):
		cancelWriter()
		writerErr = <-writerDone
	}
	cancelWriter()
	coordinator.Close()
	cancelCoordinator()
	coordinatorErr := <-coordinatorDone
	checkpointErr := db.Checkpoint(context.Background())
	return errors.Join(serverErr, writerErr, coordinatorErr, checkpointErr)
}

func (a *App) ensureDataDir() error {
	if err := os.MkdirAll(a.cfg.DataDir, 0750); err != nil {
		return fmt.Errorf("creating data directory %q: %w", a.cfg.DataDir, err)
	}
	return a.cfg.IsWritableDataDir()
}

func (a *App) controlSocketPath() string {
	return filepath.Join(a.cfg.DataDir, "siftail-control.sock")
}

func (a *App) openControlSocket() (net.Listener, error) {
	return openOwnerOnlyControlSocket(a.controlSocketPath())
}

func (a *App) controlMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})
	store := sources.NewCoordinatedStore(a.db.Reader(), a.coordinator)
	administratorStore := auth.NewCoordinatedStore(a.db.Reader(), a.coordinator)
	sessionStore := sessions.NewCoordinatedStore(a.db.Reader(), a.coordinator)
	mux.HandleFunc("POST /administrator", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeControlJSON(w, r, &input) {
			return
		}
		administrator, err := administratorStore.Create(r.Context(), input.Username, []byte(input.Password))
		writeControlJSON(w, administrator, err)
	})
	mux.HandleFunc("POST /administrator/reset-password", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Password string `json:"password"`
		}
		if !decodeControlJSON(w, r, &input) {
			return
		}
		writeControlJSON(w, struct{}{}, administratorStore.ResetPassword(r.Context(), []byte(input.Password)))
	})
	mux.HandleFunc("POST /sessions/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		affected, err := sessionStore.RevokeAll(r.Context(), 1)
		writeControlJSON(w, struct {
			Revoked int64 `json:"revoked"`
		}{Revoked: affected}, err)
	})
	mux.HandleFunc("POST /servers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name     string `json:"name"`
			Hostname string `json:"hostname"`
		}
		if !decodeControlJSON(w, r, &input) {
			return
		}
		server, err := store.CreateServer(r.Context(), input.Name, input.Hostname)
		writeControlJSON(w, server, err)
	})
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		servers, err := store.ListServers(r.Context())
		writeControlJSON(w, servers, err)
	})
	mux.HandleFunc("POST /tokens", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ServerID int64  `json:"server_id"`
			Name     string `json:"name"`
		}
		if !decodeControlJSON(w, r, &input) {
			return
		}
		token, err := store.CreateToken(r.Context(), input.ServerID, input.Name)
		writeControlJSON(w, token, err)
	})
	mux.HandleFunc("POST /tokens/revoke", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID int64 `json:"id"`
		}
		if !decodeControlJSON(w, r, &input) {
			return
		}
		writeControlJSON(w, struct{}{}, store.RevokeToken(r.Context(), input.ID))
	})
	return mux
}

func (a *App) runControlServer(ctx context.Context, l net.Listener) error {
	srv := a.newServer(a.controlMux(), "control")

	a.logger.Info("component started", "component", "control")
	return a.serveHTTP(ctx, srv, l, "control")
}

func (a *App) uiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if a.shuttingDown.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	a.browser.Register(mux)
	return mux
}

func (a *App) ingestMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/ingest", a.ingestHTTP)
	return mux
}

func (a *App) initializeIngestion() error {
	store := sources.NewStore(a.db.Reader())
	admission := ingest.NewAdmission(ingest.AdmissionLimits{
		MaxDecoders:       int(a.cfg.IngestMaxDecoders),
		ResidentMaxEvents: a.cfg.IngestResidentMaxEvents,
		ResidentMaxBytes:  a.cfg.IngestResidentMaxBytes,
		QueueMaxEvents:    a.cfg.QueueMaxEvents,
		QueueMaxBytes:     a.cfg.QueueMaxBytes,
	})
	decoder := ingest.NewJSONDecoder(ingest.DecoderLimits{
		MaxCompressedBytes:   a.cfg.MaxCompressedRequestBytes,
		MaxDecompressedBytes: a.cfg.MaxDecompressedRequestBytes,
		MaxEventBytes:        a.cfg.MaxEventBytes,
		MaxEvents:            int(a.cfg.MaxEventsPerRequest),
		MaxJSONDepth:         32,
	}).WithAdmission(admission)
	queue := ingest.NewQueue(admission)
	handler := ingest.NewHandler(store, decoder, ingest.Limits{
		MaxCompressedBytes: a.cfg.MaxCompressedRequestBytes,
		RequestTimeout:     30 * time.Second,
	}).WithQueue(queue)
	a.admission = admission
	a.queue = queue
	a.ingestHTTP = handler
	return nil
}

func (a *App) newServer(handler http.Handler, name string) *http.Server {
	base := web.RequestID(
		web.PanicRecovery(a.logger)(
			web.RequestLogger(a.logger)(
				web.WithCommonHeaders(handler),
			),
		),
	)
	return &http.Server{
		Handler:           base,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		ErrorLog: log.New(
			safeHTTPErrorWriter{logger: a.logger, component: name},
			"",
			0,
		),
	}
}

func (a *App) runUIServer(ctx context.Context) error {
	srv := a.newServer(auth.SecurityHeaders(a.cfg.PublicURL)(a.uiMux()), "ui")
	return a.runHTTPServer(ctx, srv, a.cfg.UIAddr, "ui")
}

func (a *App) runIngestServer(ctx context.Context) error {
	srv := a.newServer(a.ingestMux(), "ingest")
	return a.runHTTPServer(ctx, srv, a.cfg.IngestAddr, "ingest")
}

func (a *App) runHTTPServer(ctx context.Context, srv *http.Server, addr, name string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s listener: %w", name, err)
	}

	a.logger.Info("component started", "component", name)
	return a.serveHTTP(ctx, srv, listener, name)
}

func (a *App) serveHTTP(ctx context.Context, srv *http.Server, listener net.Listener, name string) error {
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- srv.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("%s server: %w", name, err)
		}
		if ctx.Err() == nil {
			return fmt.Errorf("%s server stopped unexpectedly", name)
		}
		return nil
	case <-ctx.Done():
		a.shuttingDown.Store(true)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		a.logger.Error("server shutdown timed out",
			"component", name,
			"error_category", "shutdown_timeout",
		)
		if closeErr := srv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return fmt.Errorf("%s forced close: %w", name, closeErr)
		}
	}

	serveErr := <-serveResult
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("%s server shutdown exceeded %s: %w", name, a.cfg.ShutdownTimeout, shutdownErr)
	}
	return nil
}

type safeHTTPErrorWriter struct {
	logger    *slog.Logger
	component string
}

func decodeControlJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeControlJSON(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		http.Error(w, "administrative operation failed", http.StatusBadRequest)
		return
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "administrative operation failed", http.StatusInternalServerError)
	}
}

func (w safeHTTPErrorWriter) Write(message []byte) (int, error) {
	w.logger.Warn("http server reported a connection error",
		"component", w.component,
		"error_category", "http_connection",
	)
	return len(message), nil
}
