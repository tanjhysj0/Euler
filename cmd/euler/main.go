// Command euler runs the local single-user agent workbench backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"euler/internal/agent"
	"euler/internal/api"
	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/session"
	"euler/internal/workspace"
	"euler/web"
)

const (
	exitFailure       = 1
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
	serverErrBuffer   = 1
	logKeyError       = "error"
)

func main() {
	configPath := flag.String("config", defaultConfigPath(), "path to config file")
	flag.Parse()
	if err := serve(*configPath); err != nil {
		slog.Error("euler startup failed", "error", err)
		os.Exit(exitFailure)
	}
}

func serve(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Generate the per-process token eagerly so startup fails fast when
	// crypto/rand is unavailable. The token is never logged nor persisted.
	auth, err := api.NewLocalAuth(cfg.AllowedOrigins)
	if err != nil {
		return fmt.Errorf("create local auth: %w", err)
	}
	if err = persistence.Migrate(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	db, err := persistence.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer closeDatabase(db)
	svc := buildServices(cfg, db)
	if err = svc.runs.RecoverInterrupted(context.Background()); err != nil {
		return fmt.Errorf("recover interrupted runs: %w", err)
	}
	router := registerRoutes(cfg, auth, db, svc)
	slog.Info("config loaded", "listen", cfg.Listen, "allowed_origins", len(cfg.AllowedOrigins))
	return serveHTTP(router, svc.runs, cfg.Listen)
}

// services carries the assembled domain services between startup steps.
type services struct {
	profiles  *modelprofile.Service
	sessions  *session.Service
	events    *event.Store
	approvals *approval.Service
	runStore  *run.Store
	runs      *run.Manager
}

func buildServices(cfg config.Config, db *gorm.DB) services {
	profiles := modelprofile.NewService(
		modelprofile.NewGormRepository(db), modelprofile.KeychainStore{},
	)
	workspaces := workspace.NewService(cfg.WorkspaceRoot)
	events := event.NewStore(db, event.NewHub())
	approvals := approval.NewService(approval.NewDBStore(db), events)
	runStore := run.NewStore(db, events, persistence.HealthChecker{DB: db})
	runner := agent.NewRuntime(agent.Deps{
		DB: db, Events: events, Gate: approvals,
		Profiles: profiles, Workspaces: workspace.DBWorkspaceFinder{DB: db},
		Limits: cfg.Agent, ShellTimeout: cfg.ShellTimeout,
		ShellOutputBytes: cfg.ShellOutputBytes, PreviewBytes: cfg.PreviewBytes,
	})
	return services{
		profiles:  profiles,
		sessions:  session.NewService(db, workspaces),
		events:    events,
		approvals: approvals,
		runStore:  runStore,
		runs:      run.NewManager(runStore, runner, run.Limits{MaxDuration: cfg.Agent.MaxDuration}),
	}
}

func registerRoutes(
	cfg config.Config, auth *api.LocalAuth, db *gorm.DB, svc services,
) *gin.Engine {
	router, protected := api.NewRouter(persistence.HealthChecker{DB: db}, auth)
	modelprofile.NewHandler(svc.profiles).Register(protected)
	workspace.NewHandler().Register(protected)
	workspace.NewFileHandler(
		workspace.DBWorkspaceFinder{DB: db}, cfg.PreviewBytes,
	).Register(protected)
	session.NewHandler(svc.sessions).Register(protected)
	run.NewHandler(svc.runs, svc.runStore, svc.events).Register(protected)
	approval.NewHandler(svc.approvals).Register(protected)
	api.RegisterStatic(router, web.Dist())
	return router
}

// serveHTTP runs the server until SIGINT/SIGTERM, then drains HTTP and the
// run manager before returning so deferred cleanup closes the database.
func serveHTTP(router *gin.Engine, runs *run.Manager, addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, serverErrBuffer)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
		shutdown(server, runs)
		return nil
	}
}

func shutdown(server *http.Server, runs *run.Manager) {
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelHTTP()
	if err := server.Shutdown(httpCtx); err != nil {
		slog.Warn("shutdown http server", logKeyError, err)
	}
	runCtx, cancelRuns := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelRuns()
	if err := runs.Shutdown(runCtx); err != nil {
		slog.Warn("shutdown run manager", logKeyError, err)
	}
}

func closeDatabase(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err != nil {
		slog.Warn("get sql db for close", logKeyError, err)
		return
	}
	if err = sqlDB.Close(); err != nil {
		slog.Warn("close database", logKeyError, err)
	}
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".euler", "config.yaml")
}
