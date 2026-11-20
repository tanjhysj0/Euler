// Command euler 运行本地单用户 agent 工作台的后端。
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
	"euler/internal/tracing"
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
	// 提前生成进程级 token，以便在 crypto/rand 不可用时启动立即失败。
	// 该 token 绝不会被记录到日志或持久化。
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
	return serveHTTP(router, svc.runs, svc.tracer, cfg.Listen)
}

// services 在各启动步骤之间承载组装好的领域服务。
type services struct {
	profiles  *modelprofile.Service
	sessions  *session.Service
	events    *event.Store
	approvals *approval.Service
	runStore  *run.Store
	runs      *run.Manager
	tracer    *tracing.Tracer
}

func buildServices(cfg config.Config, db *gorm.DB) services {
	profiles := modelprofile.NewService(
		modelprofile.NewGormRepository(db), modelprofile.KeychainStore{},
	)
	workspaces := workspace.NewService(cfg.WorkspaceRoot)
	events := event.NewStore(db, event.NewHub())
	approvals := approval.NewService(approval.NewDBStore(db), events)
	runStore := run.NewStore(db, events, persistence.HealthChecker{DB: db})
	workspaceFinder := workspace.DBWorkspaceFinder{DB: db}
	tracer := newTracer(cfg.Langfuse)
	// 审批功能暂时关闭：AutoApprove 直接放行敏感工具，工具耗时不再包含人工等待。
	// 需要恢复人工审核时，把 Gate 换回 approvals 服务即可。
	runner := agent.NewRuntime(agent.Deps{
		Loader: agent.NewDBLoader(db, profiles, workspaceFinder, cfg.SystemPromptTemplate),
		Events: events, Gate: approval.AutoApprove{}, Tracer: asRunTracer(tracer),
		Limits: cfg.Agent, ShellTimeout: cfg.ShellTimeout,
		ShellOutputBytes: cfg.ShellOutputBytes, PreviewBytes: cfg.PreviewBytes,
		ShellPathPrefix: cfg.ShellPathPrefix, ModelTimeout: cfg.ModelTimeout,
		ModelReasoningEffort: cfg.ModelReasoningEffort,
	})
	return services{
		profiles:  profiles,
		sessions:  session.NewService(db, workspaces),
		events:    events,
		approvals: approvals,
		runStore:  runStore,
		runs: run.NewManager(
			runStore, runner, run.Limits{MaxDuration: cfg.Agent.MaxDuration},
			asManagerTracer(tracer),
		),
		tracer: tracer,
	}
}

// asRunTracer 把可能为 nil 的 tracer 转为接口；直接赋值会得到非 nil 的
// typed nil 接口，使调用方对 nil 的判断失效。
func asRunTracer(tracer *tracing.Tracer) agent.RunTracer {
	if tracer == nil {
		return nil
	}
	return tracer
}

// asManagerTracer 与 asRunTracer 同理，用于 run 层的追踪接口。
func asManagerTracer(tracer *tracing.Tracer) run.Tracer {
	if tracer == nil {
		return nil
	}
	return tracer
}

// newTracer 在配置了 Langfuse 时创建追踪器；初始化失败只降级关闭追踪，
// 不影响 agent 主流程。
func newTracer(cfg config.LangfuseConfig) *tracing.Tracer {
	if !cfg.Enabled() {
		return nil
	}
	tracer, err := tracing.New(tracing.Config{
		Host: cfg.Host, PublicKey: cfg.PublicKey,
		SecretKey: cfg.SecretKey, Environment: cfg.Environment,
	})
	if err != nil {
		slog.Warn("disable langfuse tracing", logKeyError, err)
		return nil
	}
	slog.Info("langfuse tracing enabled", "host", cfg.Host)
	return tracer
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

// serveHTTP 运行服务器直到收到 SIGINT/SIGTERM，然后排空 HTTP 和
// run manager 再返回，使延迟执行的清理逻辑关闭数据库。
func serveHTTP(
	router *gin.Engine, runs *run.Manager, tracer *tracing.Tracer, addr string,
) error {
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
		shutdown(server, runs, tracer)
		return nil
	}
}

func shutdown(server *http.Server, runs *run.Manager, tracer *tracing.Tracer) {
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
	if tracer != nil {
		// run 结束后再刷新，确保最后一次 run 的 trace 也能上传
		tracer.Flush()
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
