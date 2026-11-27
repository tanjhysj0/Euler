// Command euler 运行本地单用户 agent 工作台的后端。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
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
	"euler/internal/tray"
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

// version 由构建流程通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	configPath := flag.String("config", defaultConfigPath(), "配置文件路径")
	openBrowser := flag.Bool("open", envBool(envOpenBrowser), "启动后自动用浏览器打开界面")
	useTray := flag.Bool("tray", envBool(envTray), "在系统托盘/菜单栏常驻，不占用终端窗口")
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()
	if *showVersion {
		if _, err := fmt.Fprintln(os.Stdout, "euler", version); err != nil {
			slog.Warn("print version failed", logKeyError, err)
		}
		return
	}
	if err := serve(*configPath, serveOptions{
		openBrowser: *openBrowser, useTray: *useTray,
	}); err != nil {
		slog.Error("euler startup failed", "error", err)
		os.Exit(exitFailure)
	}
}

// serveOptions 汇总启动阶段的一次性开关，避免布尔参数直接控制分支。
type serveOptions struct {
	openBrowser bool
	useTray     bool
}

func serve(configPath string, opts serveOptions) error {
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
	// 启动即校验工作区根目录可创建、可写，避免创建会话时才暴露权限问题。
	if err = workspace.NewService(cfg.WorkspaceRoot).EnsureRoot(); err != nil {
		return fmt.Errorf("prepare workspace root: %w", err)
	}
	if err = persistence.Migrate(cfg.DatabaseDriver, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	db, err := persistence.Open(context.Background(), cfg.DatabaseDriver, cfg.DatabaseURL)
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
	runnable := httpServer{
		router: router, runs: svc.runs, tracer: svc.tracer,
		opts: httpServerOptions{listen: cfg.Listen, openBrowser: opts.openBrowser},
	}
	// 托盘模式把 HTTP 服务放后台、主 goroutine 交给系统托盘事件循环；
	// 无托盘实现的平台自动回退到前台运行。
	if opts.useTray && tray.Supported() {
		return runnable.runWithTray()
	}
	return runnable.runForeground()
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
	models := agent.OpenAIChatModelFactory{
		Timeout: cfg.ModelTimeout, RequestTimeout: cfg.ModelRequestTimeout,
		ReasoningEffort: cfg.ModelReasoningEffort,
	}
	runner := agent.NewRuntime(agent.Deps{
		Loader: agent.NewDBLoader(db, profiles, workspaceFinder, cfg.SystemPromptTemplate),
		Events: events, Gate: approval.AutoApprove{}, Tracer: asRunTracer(tracer),
		Limits: cfg.Agent, ShellTimeout: cfg.ShellTimeout,
		ShellOutputBytes: cfg.ShellOutputBytes, PreviewBytes: cfg.PreviewBytes,
		ShellPathPrefix: cfg.ShellPathPrefix, Factory: models,
		ModelTimeout: cfg.ModelTimeout, ModelRequestTimeout: cfg.ModelRequestTimeout,
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
			run.Deps{
				Tracer: asManagerTracer(tracer),
				Titles: session.NewTitleNamer(db, profiles, models),
			},
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

// httpServerOptions 汇总 HTTP 服务启动参数，避免函数参数过多。
type httpServerOptions struct {
	listen      string
	openBrowser bool
}

// httpServer 汇总启动 HTTP 服务所需的依赖，便于前台与托盘两种运行方式复用。
type httpServer struct {
	router *gin.Engine
	runs   *run.Manager
	tracer *tracing.Tracer
	opts   httpServerOptions
}

// runForeground 以终端前台方式运行，收到 SIGINT/SIGTERM 后退出。
func (s httpServer) runForeground() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return s.run(ctx)
}

// runWithTray 在系统托盘常驻运行：菜单可打开界面或退出进程。
// macOS 的 Cocoa 事件循环要求托盘占用主 goroutine，因此服务放到后台，
// 两个方向的退出（托盘菜单 / 系统信号）都通过取消 ctx 统一收尾。
func (s httpServer) runWithTray() error {
	target, err := uiURL(s.opts.listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serveErr := make(chan error, serverErrBuffer)
	go func() { serveErr <- s.run(ctx) }()
	// 服务结束或收到系统信号时关闭托盘循环；托盘退出菜单则取消 ctx。
	stopTray := make(chan struct{})
	failure := make(chan error, serverErrBuffer)
	go func() {
		var runErr error
		select {
		case <-sigCtx.Done():
			cancel()
			runErr = <-serveErr
		case runErr = <-serveErr:
		}
		failure <- runErr
		close(stopTray)
	}()
	tray.Run(tray.Options{
		URL:    target,
		OnOpen: func() { openUI(s.opts.listen) },
		OnQuit: cancel,
	}, stopTray)
	return <-failure
}

// run 启动服务器并阻塞到 ctx 取消或服务出错，随后排空 HTTP 与 run manager。
func (s httpServer) run(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.opts.listen,
		Handler:           s.router,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	// 先显式监听，确保端口就绪后再打开浏览器，避免页面先于服务加载。
	listener, err := net.Listen("tcp", s.opts.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.listen, err)
	}
	errCh := make(chan error, serverErrBuffer)
	go func() { errCh <- server.Serve(listener) }()
	if s.opts.openBrowser {
		go openUI(s.opts.listen)
	}
	select {
	case err = <-errCh:
		return wrapServeError(err)
	case <-ctx.Done():
		shutdown(ctx, server, s.runs, s.tracer)
		return nil
	}
}

// wrapServeError 把正常关闭排除在外，便于调用方区分真实故障。
func wrapServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve http: %w", err)
}

// shutdown 依次排空 HTTP 服务、run manager 与追踪器。
// parent 通常已被取消，因此用 WithoutCancel 保留其取值后重新计时。
func shutdown(
	parent context.Context, server *http.Server, runs *run.Manager, tracer *tracing.Tracer,
) {
	base := context.WithoutCancel(parent)
	httpCtx, cancelHTTP := context.WithTimeout(base, shutdownTimeout)
	defer cancelHTTP()
	if err := server.Shutdown(httpCtx); err != nil {
		slog.Warn("shutdown http server", logKeyError, err)
	}
	runCtx, cancelRuns := context.WithTimeout(base, shutdownTimeout)
	defer cancelRuns()
	if err := runs.Shutdown(runCtx); err != nil {
		slog.Warn("shutdown run manager", logKeyError, err)
	}
	if tracer != nil {
		// run 结束后再刷新，确保最后一次 run 的 trace 也能上传
		tracer.Flush(base)
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
