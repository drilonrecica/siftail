package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/web"
	"golang.org/x/sync/errgroup"
)

// App composes the long-running Siftail process.
type App struct {
	cfg    config.Config
	logger *slog.Logger
}

// New creates an application root. It does not open listeners or databases.
func New(cfg config.Config, logger *slog.Logger) *App {
	return &App{cfg: cfg, logger: logger}
}

// Run starts all critical components and blocks until the context is cancelled
// or a critical component fails. The first critical error cancels the
// application context.
func (a *App) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	if err := a.ensureDataDir(); err != nil {
		return err
	}

	controlListener, err := a.openControlSocket()
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer func() {
		_ = controlListener.Close()
		_ = os.Remove(a.controlSocketPath())
	}()

	g.Go(func() error { return a.runControlServer(ctx, controlListener) })
	g.Go(func() error { return a.runUIServer(ctx) })
	g.Go(func() error { return a.runIngestServer(ctx) })

	return g.Wait()
}

func (a *App) ensureDataDir() error {
	if err := os.MkdirAll(a.cfg.DataDir, 0750); err != nil {
		return fmt.Errorf("creating data directory %q: %w", a.cfg.DataDir, err)
	}
	return nil
}

func (a *App) controlSocketPath() string {
	return filepath.Join(a.cfg.DataDir, "siftail-control.sock")
}

func (a *App) openControlSocket() (net.Listener, error) {
	path := a.controlSocketPath()
	// Remove stale socket left by a previous unclean shutdown.
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale control socket %q: %w", path, err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("setting control socket permissions: %w", err)
	}
	return l, nil
}

func (a *App) controlMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})
	return mux
}

func (a *App) runControlServer(ctx context.Context, l net.Listener) error {
	mux := a.controlMux()
	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	a.logger.Info("control socket listening", "path", a.controlSocketPath())
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("control server: %w", err)
	}
	return nil
}

func (a *App) uiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (a *App) ingestMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
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
		Handler:      base,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		ErrorLog:     slog.NewLogLogger(a.logger.Handler(), slog.LevelError),
	}
}

func (a *App) runUIServer(ctx context.Context) error {
	srv := a.newServer(a.uiMux(), "ui")
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

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			a.logger.Error("server shutdown error", "server", name, "error", err)
		}
	}()

	a.logger.Info("server listening", "server", name, "addr", listener.Addr().String())
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}
