# Euler 基础工作台 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 macOS 上交付本机单用户、多会话的通用 Agent 工作台，贯通工作区、OpenAI 兼容流式对话、文件与 Shell 审批、持久化恢复和产物预览。

**Architecture:** Go 单进程模块化单体提供 REST、SSE、Agent 执行与前端静态资源，PostgreSQL 保存权威状态。Eino ReAct 负责模型与工具循环；文件和 Shell 通过独立工具层执行，敏感操作由持久化审批阻塞。React 使用 TanStack Query 保存服务端快照、Zustand 保存流式增量和 UI 状态。

**Tech Stack:** Go 1.26、Gin、GORM、PostgreSQL、golang-migrate、Eino v0.9.23、Eino OpenAI v0.1.13、React、TypeScript、Vite、Ant Design、TanStack Query、Zustand、Vitest、Playwright。

**Spec:** `docs/superpowers/specs/2026-11-17-euler-agent-workbench-design.md`

## Global Constraints

- 本机单用户；默认监听 `127.0.0.1:8080`，第一阶段只在 macOS 做端到端验收。
- Go module 名为 `euler`；后端内部导入统一使用 `euler/internal/...`。
- 固定 `github.com/cloudwego/eino v0.9.23` 与 `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`；执行 `go mod tidy` 时不得升级这两项。
- Eino `StreamReader` 只能读取一次且必须关闭；ReAct 的 `Agent.Stream` 只提供最终输出，中间轮次必须通过 `react.WithMessageFuture` 消费。
- 配置文件默认为 `~/.euler/config.yaml`；数据库只从 `EULER_DATABASE_URL` 读取；默认工作区为 `~/EulerWorkspaces/<session-id>`。
- 每个会话最多一个 `queued`、`running` 或 `waiting_approval` Run；同一路径可被不同会话复用，不做全局工作区锁。
- 文件读、列目录和搜索自动执行；写入、补丁、删除及每次 Shell 调用都必须先审批，拒绝不得产生副作用。
- Shell 不是沙箱；必须过滤模型配置引用的环境密钥，支持进程组取消、超时和输出截断。
- 事件先写 PostgreSQL，再发布到内存 Hub；SSE 以序号为 `id`，回放与实时事件按 `run_id + seq` 去重并补洞。
- API Key 只存在于密钥适配器和模型调用的内存中，不写入数据库、事件、API 响应或日志。
- 自动化测试使用真实 PostgreSQL、临时目录和模拟 OpenAI 服务，不调用真实付费模型。
- 每个行为先写失败测试，再写最小实现；Go 代码遵守现有硬门禁：函数不超过 60 行/40 语句、认知复杂度不超过 15、参数不超过 4 个、返回值不超过 3 个、行宽不超过 100。
- `.golangci.yml` 中的旧项目名与 import 前缀统一改为 `euler`，保留现有质量规则，不通过添加豁免绕过问题。
- 当前目录不是 Git 仓库；本计划不初始化 Git、不执行 commit，也不包含 commit 步骤。
- 第一阶段不加入子 Agent、MCP、长期记忆、pgvector、OpenTelemetry、自动续跑或 Docker Agent 沙箱。

## 文件结构与职责

| 文件 | 职责 |
| --- | --- |
| `cmd/euler/main.go` | 配置、依赖装配、迁移、恢复、HTTP 与优雅退出 |
| `internal/config/config.go` | 默认配置、YAML 与环境变量加载、限制校验 |
| `internal/persistence/{db.go,models.go}` | PostgreSQL 连接、GORM 实体和迁移 |
| `migrations/000001_init.{up,down}.sql` | 权威数据库 schema 与约束 |
| `internal/api/{router.go,errors.go,middleware.go,sse.go}` | 路由、统一错误、本机访问保护和 SSE |
| `internal/modelprofile/{service.go,secret.go,handler.go}` | 模型配置、Keychain/环境密钥引用和 API |
| `internal/workspace/{service.go,fs.go,handler.go}` | 路径规范化、目录创建、目录浏览和安全文件访问 |
| `internal/session/{service.go,handler.go}` | 会话、消息分页、路径复用与工作区警告 |
| `internal/event/{store.go,hub.go,types.go}` | 事件序号、落库、发布、回放和补洞 |
| `internal/run/{types.go,store.go,manager.go,handler.go}` | Run 状态机、并发、取消、快照和启动恢复 |
| `internal/approval/{service.go,handler.go}` | 审批持久化、等待、决策和取消联动 |
| `internal/tools/{file.go,shell.go,registry.go,process_darwin.go}` | 文件工具、Shell、输出限制和 Eino 工具注册 |
| `internal/agent/{runtime.go,model.go,stream.go,tools.go}` | Eino ReAct、模型重试/限轮、消息流和工具观察 |
| `internal/logging/redact.go` | 密钥与敏感文本脱敏 |
| `web/src/api/client.ts` | 同源 fetch、bootstrap、错误与请求封装 |
| `web/src/stores/run.ts` | 流式增量、SSE 去重、连接状态、草稿 |
| `web/src/features/sessions/*` | 会话列表、新建会话和目录选择 |
| `web/src/features/chat/*` | Markdown 消息、Run 控制、工具时间线和审批 |
| `web/src/features/files/*` | 文件树、文本/Markdown/图片预览和下载 |
| `web/src/features/settings/*` | 模型配置和只写密钥表单 |
| `web/embed.go` | Go 托管 `web/dist` 静态资源 |
| `web/tests/*` | 模拟 OpenAI 与 Playwright 端到端验收 |

## 核心接口约定

后续任务共同使用下列签名；实现时不得改名或改变语义。

```go
// internal/event
type Publisher interface {
    Append(ctx context.Context, runID uuid.UUID, eventType string, payload any) (Event, error)
    ListAfter(ctx context.Context, runID uuid.UUID, after int64) ([]Event, error)
    Subscribe(runID uuid.UUID) (<-chan Event, func())
}

// internal/run
type Request struct {
    RunID              uuid.UUID
    SessionID          uuid.UUID
    WorkspaceID        uuid.UUID
    AssistantMessageID uuid.UUID
}
type Runner interface {
    Run(ctx context.Context, request Request) error
}

// internal/modelprofile
type ResolvedProfile struct {
    ID uuid.UUID
    BaseURL string
    Model string
    APIKey string
}
type Resolver interface {
    Resolve(ctx context.Context, profileID uuid.UUID) (ResolvedProfile, error)
    EnvironmentSecretNames(ctx context.Context) ([]string, error)
}

// internal/approval
type Request struct {
    RunID uuid.UUID
    ToolCallID uuid.UUID
    Name string
    Payload any
}
type Decision struct {
    Approved bool
    Reason string
}
type Gate interface {
    Require(ctx context.Context, request Request) (Decision, error)
}

// internal/agent
type ChatModelFactory interface {
    New(ctx context.Context, profile modelprofile.ResolvedProfile) (model.ToolCallingChatModel, error)
}
```

---

### Task 1: 工程骨架、配置与质量门禁

**Files:**
- Create: `go.mod`、`cmd/euler/main.go`、`internal/config/config.go`、`internal/config/config_test.go`、`compose.yaml`、`Makefile`
- Modify: `.golangci.yml:1-4`、`.golangci.yml:1203-1218`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Load(path string) (Config, error)`；`Config.Agent` 给 Run 与 Agent；`Config.WorkspaceRoot` 给会话服务。

- [ ] **Step 1: 写默认配置与限制校验的失败测试**

```go
func TestLoadDefaults(t *testing.T) {
    t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
    cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
    if err != nil { t.Fatal(err) }
    if cfg.Listen != "127.0.0.1:8080" { t.Fatalf("listen=%q", cfg.Listen) }
    if cfg.DatabaseURL == "" || cfg.Agent.MaxModelRounds != 12 { t.Fatalf("cfg=%+v", cfg) }
    if !strings.HasSuffix(cfg.WorkspaceRoot, "EulerWorkspaces") { t.Fatal(cfg.WorkspaceRoot) }
}

func TestLoadRejectsInvalidLimits(t *testing.T) {
    t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
    path := filepath.Join(t.TempDir(), "config.yaml")
    body := []byte("agent:\n  max_model_rounds: 0\n  max_tool_calls: 40\n  max_duration: 15m\n  max_output_bytes: 65536\n")
    if err := os.WriteFile(path, body, 0o600); err != nil { t.Fatal(err) }
    if _, err := Load(path); err == nil { t.Fatal("expected validation error") }
}
```

- [ ] **Step 2: 运行测试并确认编译失败**

Run: `go test ./internal/config`
Expected: FAIL，当前不存在 `go.mod` 与 `Load`。

- [ ] **Step 3: 初始化 Go 工程并安装固定底座**

```bash
go mod init euler
go get github.com/cloudwego/eino@v0.9.23
go get github.com/cloudwego/eino-ext/components/model/openai@v0.1.13
go get github.com/gin-gonic/gin github.com/google/uuid gopkg.in/yaml.v3
go get gorm.io/gorm gorm.io/driver/postgres gorm.io/datatypes
go get github.com/golang-migrate/migrate/v4
go get github.com/cyphar/filepath-securejoin github.com/zalando/go-keyring
go get github.com/pmezard/go-difflib github.com/stretchr/testify
```

将 `.golangci.yml` 顶部两处 `novel-platform-api` 改为 `Euler`，将 `local-prefixes` 改为 `- euler`，将 gci 最后一组改为 `prefix(euler)`。`compose.yaml` 只定义 `postgres:17`，数据库/用户/密码均为 `euler`，端口为 `5432:5432`，数据卷为 `euler-postgres`，健康检查使用 `pg_isready -U euler`。

- [ ] **Step 4: 实现配置结构和加载**

```go
type AgentLimits struct {
    MaxModelRounds int           `yaml:"max_model_rounds"`
    MaxToolCalls   int           `yaml:"max_tool_calls"`
    MaxDuration    time.Duration `yaml:"max_duration"`
    MaxOutputBytes int           `yaml:"max_output_bytes"`
}

type Config struct {
    Listen        string        `yaml:"listen"`
    WorkspaceRoot string        `yaml:"workspace_root"`
    AllowedOrigins []string     `yaml:"allowed_origins"`
    DatabaseURL   string        `yaml:"-"`
    Agent         AgentLimits   `yaml:"agent"`
    ShellTimeout  time.Duration `yaml:"shell_timeout"`
    ShellOutputBytes int        `yaml:"shell_output_bytes"`
    PreviewBytes  int           `yaml:"preview_bytes"`
}

func Load(path string) (Config, error) {
    home, err := os.UserHomeDir()
    if err != nil { return Config{}, fmt.Errorf("home: %w", err) }
    cfg := Config{
        Listen: "127.0.0.1:8080",
        WorkspaceRoot: filepath.Join(home, "EulerWorkspaces"),
        Agent: AgentLimits{12, 40, 15 * time.Minute, 64 * 1024},
        ShellTimeout: 2 * time.Minute,
        ShellOutputBytes: 1024 * 1024,
        PreviewBytes: 1024 * 1024,
    }
    data, err := os.ReadFile(path)
    if err != nil && !errors.Is(err, os.ErrNotExist) { return Config{}, err }
    if len(data) > 0 {
        if err = yaml.Unmarshal(data, &cfg); err != nil { return Config{}, err }
    }
    cfg.DatabaseURL = os.Getenv("EULER_DATABASE_URL")
    if cfg.DatabaseURL == "" { return Config{}, errors.New("EULER_DATABASE_URL is required") }
    if cfg.Agent.MaxModelRounds < 1 || cfg.Agent.MaxToolCalls < 1 ||
        cfg.Agent.MaxDuration <= 0 || cfg.Agent.MaxOutputBytes < 1024 {
        return Config{}, errors.New("invalid agent limits")
    }
    if !strings.HasPrefix(cfg.Listen, "127.0.0.1:") && !strings.HasPrefix(cfg.Listen, "localhost:") {
        return Config{}, errors.New("listen must be loopback in phase one")
    }
    return cfg, nil
}
```

`main.go` 先只解析 `-config`，默认路径为 `~/.euler/config.yaml`，调用 `Load` 并以 `slog` 输出不含 DatabaseURL 的启动错误；后续任务在同一装配函数中接入服务。

- [ ] **Step 5: 验证配置与 lint**

```bash
go test ./internal/config
golangci-lint config verify
gofmt -w cmd/euler/main.go internal/config/config.go internal/config/config_test.go
go test ./...
```

Expected: 全部 PASS；lint 配置验证无输出。

---

### Task 2: PostgreSQL schema、迁移与数据库健康检查

**Files:**
- Create: `migrations/000001_init.up.sql`、`migrations/000001_init.down.sql`
- Create: `internal/persistence/db.go`、`internal/persistence/models.go`、`internal/persistence/db_integration_test.go`
- Create: `internal/api/router.go`、`internal/api/router_test.go`
- Test: `internal/persistence/db_integration_test.go`

**Interfaces:**
- Produces: `persistence.Open(ctx context.Context, databaseURL string) (*gorm.DB, error)`；`persistence.Migrate(databaseURL string) error`；GORM 实体供各服务使用。

- [ ] **Step 1: 写迁移、活跃 Run 唯一约束和消息序号的集成测试**

测试文件首行使用 `//go:build integration`。`openTestDB` 在缺少 `TEST_DATABASE_URL` 时调用 `t.Fatal`，防止集成测试静默跳过。

```go
func TestMigrationAndActiveRunConstraint(t *testing.T) {
    db := openTestDB(t)
    sessionID := insertSessionFixture(t, db)
    first := Run{ID: uuid.New(), SessionID: sessionID, Status: "queued", NextEventSeq: 1}
    if err := db.Create(&first).Error; err != nil { t.Fatal(err) }
    second := Run{ID: uuid.New(), SessionID: sessionID, Status: "running", NextEventSeq: 1}
    if err := db.Create(&second).Error; err == nil { t.Fatal("expected unique active run violation") }
}

func TestMessageSequenceConstraint(t *testing.T) {
    db := openTestDB(t)
    sessionID := insertSessionFixture(t, db)
    row := Message{ID: uuid.New(), SessionID: sessionID, SessionSeq: 1, Role: "user", Status: "completed"}
    if err := db.Create(&row).Error; err != nil { t.Fatal(err) }
    row.ID = uuid.New()
    if err := db.Create(&row).Error; err == nil { t.Fatal("expected session sequence violation") }
}
```

- [ ] **Step 2: 启动 PostgreSQL 并确认测试失败**

```bash
docker compose up -d postgres
docker compose exec -T postgres psql -U euler -d euler -c "SELECT 'CREATE DATABASE euler_test' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='euler_test')\gexec"
export TEST_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler_test?sslmode=disable'
go test -tags=integration ./internal/persistence -run TestMigration -v
```

Expected: FAIL，迁移文件和 `Open` 尚不存在。

- [ ] **Step 3: 编写完整 schema**

`000001_init.up.sql` 创建 `workspaces`、`model_profiles`、`sessions`、`runs`、`messages`、`tool_calls`、`approvals`、`run_events`，所有 ID 为 `uuid`，时间为 `timestamptz`，负载为 `jsonb`。必须包含：

```sql
CREATE UNIQUE INDEX sessions_message_seq_idx ON messages(session_id, session_seq);
CREATE UNIQUE INDEX run_events_seq_idx ON run_events(run_id, seq);
CREATE UNIQUE INDEX runs_one_active_idx ON runs(session_id)
    WHERE status IN ('queued', 'running', 'waiting_approval');
CREATE UNIQUE INDEX approvals_tool_call_pending_idx ON approvals(tool_call_id)
    WHERE status = 'pending';
CREATE INDEX sessions_last_active_idx ON sessions(last_active_at DESC);
CREATE INDEX workspaces_path_idx ON workspaces(path);
CREATE INDEX messages_run_idx ON messages(run_id);
CREATE INDEX tool_calls_run_idx ON tool_calls(run_id, created_at);
```

状态列使用 `CHECK` 限定规格中的枚举；`messages.run_id`、`tool_calls.approval_id` 可空；`runs.next_event_seq` 默认 `1` 且检查大于 `0`；外键使用 `ON DELETE RESTRICT`。down 文件按依赖反序 drop 八张表。

- [ ] **Step 4: 实现 GORM 实体、连接与迁移**

实体字段名统一为 `Workspace`、`Session`、`ModelProfile`、`Message`、`Run`、`ToolCall`、`Approval`、`RunEvent`，通过 `TableName` 映射复数表名。JSON 使用 `datatypes.JSON`，时间使用 `time.Time`，可空时间使用 `*time.Time`。

```go
func Open(ctx context.Context, databaseURL string) (*gorm.DB, error) {
    db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Silent),
        TranslateError: true,
    })
    if err != nil { return nil, fmt.Errorf("open postgres: %w", err) }
    sqlDB, err := db.DB()
    if err != nil { return nil, err }
    sqlDB.SetMaxOpenConns(20)
    sqlDB.SetMaxIdleConns(5)
    sqlDB.SetConnMaxLifetime(30 * time.Minute)
    if err = sqlDB.PingContext(ctx); err != nil { return nil, fmt.Errorf("ping postgres: %w", err) }
    return db, nil
}
```

`Migrate` 使用 `migrate.New("file://migrations", databaseURL)` 和 `Up()`，把 `migrate.ErrNoChange` 视为成功，其他错误 `%w` 返回。增加 `type HealthChecker struct { DB *gorm.DB }`，其 `Ping(ctx)` 通过 `DB.DB()` 取得连接池并调用 `PingContext(ctx)`，供 API 健康接口和 main 装配共同使用。

- [ ] **Step 5: 增加健康接口并验证数据库**

```go
type HealthChecker interface { Ping(ctx context.Context) error }
func RegisterHealth(router *gin.Engine, checker HealthChecker) {
    router.GET("/health", func(c *gin.Context) {
        if err := checker.Ping(c.Request.Context()); err != nil {
            c.JSON(http.StatusServiceUnavailable, gin.H{"code": "database_unavailable", "message": "数据库不可用"})
            return
        }
        c.JSON(http.StatusOK, gin.H{"status": "ok"})
    })
}
```

Run: `go test ./internal/api ./internal/persistence && go test -tags=integration ./internal/persistence -v`
Expected: 单元与集成测试 PASS。

---

### Task 3: 本机 Cookie、Origin 防护与统一 API 错误

**Files:**
- Create: `internal/api/middleware.go`、`internal/api/errors.go`、`internal/api/middleware_test.go`
- Modify: `internal/api/router.go`、`cmd/euler/main.go`
- Test: `internal/api/middleware_test.go`

**Interfaces:**
- Produces: `NewLocalAuth(allowedOrigins []string) (*LocalAuth, error)`；`LocalAuth.Bootstrap()`、`LocalAuth.Require()`、`LocalAuth.CheckOrigin()` Gin 中间件。

- [ ] **Step 1: 写 Cookie 与跨源写入失败测试**

```go
func TestBootstrapCookieAndOrigin(t *testing.T) {
    auth, err := NewLocalAuth([]string{"http://127.0.0.1:5173"})
    require.NoError(t, err)
    router := gin.New()
    router.GET("/api/v1/bootstrap", auth.Bootstrap())
    protected := router.Group("/api/v1", auth.Require(), auth.CheckOrigin())
    protected.POST("/sessions", func(c *gin.Context) { c.Status(http.StatusCreated) })

    boot := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
    router.ServeHTTP(boot, req)
    require.Equal(t, http.StatusNoContent, boot.Code)
    cookies := boot.Result().Cookies()
    require.Len(t, cookies, 1)
    require.True(t, cookies[0].HttpOnly)
    require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)

    denied := httptest.NewRecorder()
    req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
    req.AddCookie(cookies[0])
    req.Header.Set("Origin", "https://evil.example")
    router.ServeHTTP(denied, req)
    require.Equal(t, http.StatusForbidden, denied.Code)
}
```

另写无 Cookie 返回 `401`、允许同源 POST 返回 `201`、bootstrap 遇到重启后的旧 Cookie 会重新设置的测试。

- [ ] **Step 2: 运行并确认失败**

Run: `go test ./internal/api -run 'TestBootstrap|TestLocalAuth' -v`
Expected: FAIL，`NewLocalAuth` 未定义。

- [ ] **Step 3: 实现本机访问保护**

使用 `crypto/rand` 生成 32 字节令牌并 base64url 编码；令牌只保存在进程内存和名为 `euler_local` 的 Cookie 中。Cookie 设置 `Path=/`、`HttpOnly=true`、`SameSite=Strict`，loopback HTTP 下 `Secure=false`。bootstrap 是无副作用的 GET，始终确保请求得到当前有效 Cookie。

`Require` 使用 `subtle.ConstantTimeCompare` 比较 Cookie；`CheckOrigin` 只拦截非 GET/HEAD/OPTIONS，要求 `Origin` 的 scheme+host 与请求同源，或精确位于 `AllowedOrigins`。统一错误结构如下，handler 通过 `AbortWithError` 返回：

```go
type ErrorBody struct {
    Code string `json:"code"`
    Message string `json:"message"`
    Details any `json:"details,omitempty"`
}
func AbortWithError(c *gin.Context, status int, code, message string) {
    c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: message})
}
```

- [ ] **Step 4: 验证安全测试**

Run: `go test ./internal/api -v && go test ./...`
Expected: PASS，未授权和跨源请求均不能进入 handler。

---

### Task 4: 模型配置与密钥引用

**Files:**
- Create: `internal/modelprofile/secret.go`、`internal/modelprofile/service.go`、`internal/modelprofile/handler.go`
- Create: `internal/modelprofile/service_test.go`、`internal/logging/redact.go`、`internal/logging/redact_test.go`
- Modify: `internal/api/router.go`、`cmd/euler/main.go`
- Test: `internal/modelprofile/service_test.go`

**Interfaces:**
- Produces: `Service.Create(ctx, CreateInput) (Profile, error)`、`Service.List(ctx) ([]Profile, error)`、`Service.Resolve(ctx, uuid.UUID) (ResolvedProfile, error)`、`Service.EnvironmentSecretNames(ctx) ([]string, error)`；实现 `modelprofile.Resolver`。

- [ ] **Step 1: 写密钥不回显和不入库测试**

```go
func TestCreateKeychainProfileDoesNotPersistSecret(t *testing.T) {
    secrets := newFakeSecretStore()
    repo := newFakeProfileRepository()
    svc := NewService(repo, secrets)
    profile, err := svc.Create(context.Background(), CreateInput{
        Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
        SecretKind: "keychain", SecretRef: "euler/test", APIKey: "sk-super-secret",
    })
    require.NoError(t, err)
    encoded, err := json.Marshal(profile)
    require.NoError(t, err)
    require.NotContains(t, string(encoded), "sk-super-secret")
    require.Equal(t, "sk-super-secret", secrets.values["euler/test"])
    require.NotContains(t, repo.lastJSON, "sk-super-secret")
}

func TestResolveEnvironmentProfile(t *testing.T) {
    t.Setenv("EULER_TEST_MODEL_KEY", "sk-from-env")
    svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
    resolved, err := svc.Resolve(context.Background(), uuid.MustParse("00000000-0000-0000-0000-000000000004"))
    require.NoError(t, err)
    require.Equal(t, "sk-from-env", resolved.APIKey)
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go test ./internal/modelprofile ./internal/logging -v`
Expected: FAIL，服务与脱敏函数未定义。

- [ ] **Step 3: 实现密钥适配器与配置服务**

```go
type SecretStore interface {
    Set(service, value string) error
    Get(service string) (string, error)
    Delete(service string) error
}
type KeychainStore struct{}
func (KeychainStore) Set(service, value string) error { return keyring.Set("Euler", service, value) }
func (KeychainStore) Get(service string) (string, error) { return keyring.Get("Euler", service) }
func (KeychainStore) Delete(service string) error { return keyring.Delete("Euler", service) }
```

`CreateInput` 包含 `Name`、`BaseURL`、`Model`、`SecretKind`、`SecretRef`、`APIKey`。keychain 模式要求 APIKey 非空，先写 Keychain，再保存不含密钥的 Profile；数据库失败时删除刚写入的 Keychain 项。environment 模式要求 `SecretRef` 匹配 `[A-Za-z_][A-Za-z0-9_]*` 且 APIKey 为空，运行时才读取环境变量。响应 DTO 只有 ID、名称、BaseURL、模型、密钥类型和引用。

`logging.Redact` 对非空密钥做完整字符串替换，并对 `Authorization`、`api_key`、`cookie` 的常见键值形式脱敏：

```go
func Redact(text string, secrets ...string) string {
    for _, secret := range secrets {
        if secret != "" { text = strings.ReplaceAll(text, secret, "[REDACTED]") }
    }
    authorization := regexp.MustCompile(`(?i)(authorization|api_key|cookie)(["\s:=]+(?:bearer\s+)?)[^\s,"}]+`)
    return authorization.ReplaceAllString(text, `${1}${2}[REDACTED]`)
}
```

- [ ] **Step 4: 接入 `GET/POST /api/v1/model-profiles` 并验证**

POST 成功返回 `201` 和持久化 DTO；参数错误返回 `400`；无法访问密钥返回 `400 secret_unavailable`。测试响应、GORM 日志参数和错误消息均不含测试密钥。

Run: `go test ./internal/modelprofile ./internal/logging ./internal/api -v`
Expected: PASS。

---

### Task 5: 工作区、目录浏览、会话与消息历史

**Files:**
- Create: `internal/workspace/service.go`、`internal/workspace/service_test.go`、`internal/workspace/handler.go`
- Create: `internal/session/service.go`、`internal/session/service_test.go`、`internal/session/handler.go`
- Modify: `internal/api/router.go`、`cmd/euler/main.go`
- Test: `internal/workspace/service_test.go`、`internal/session/service_test.go`

**Interfaces:**
- Produces: `workspace.Service.NormalizeExisting(path string) (string, error)`、`CreateDefault(sessionID uuid.UUID) (string, bool, error)`；`session.Service.Create/Get/List/Messages`。

- [ ] **Step 1: 写路径规范化和默认目录清理测试**

```go
func TestNormalizeExistingRejectsFileAndResolvesSymlink(t *testing.T) {
    root := t.TempDir()
    target := filepath.Join(root, "target")
    require.NoError(t, os.Mkdir(target, 0o700))
    link := filepath.Join(root, "link")
    require.NoError(t, os.Symlink(target, link))
    svc := NewService(root)
    got, err := svc.NormalizeExisting(link)
    require.NoError(t, err)
    resolved, _ := filepath.EvalSymlinks(target)
    require.Equal(t, resolved, got)
    require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600))
    _, err = svc.NormalizeExisting(filepath.Join(root, "file"))
    require.Error(t, err)
}

func TestDefaultWorkspaceCleanupOnlyNewEmptyDirectory(t *testing.T) {
    root := t.TempDir()
    svc := NewService(root)
    id := uuid.New()
    path, created, err := svc.CreateDefault(id)
    require.NoError(t, err)
    require.True(t, created)
    require.NoError(t, svc.CleanupCreated(path, created))
    require.NoDirExists(t, path)
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go test ./internal/workspace -v`
Expected: FAIL，`Service` 未定义。

- [ ] **Step 3: 实现路径与目录服务**

`NormalizeExisting` 只接受绝对路径，依次执行 `filepath.Clean`、`filepath.EvalSymlinks`、`os.Stat`，确认目录且进程可读写。`CreateDefault` 先以 `0700` 创建根目录，再以 `0700` 创建 `<root>/<session-id>`，返回规范化路径和 `created=true`。`CleanupCreated` 仅在 `created=true` 时调用 `os.Remove`，目录非空或不是本次创建时绝不递归删除。

目录浏览接口 `GET /api/v1/fs/directories?path=...` 默认 home，只返回当前目录、父目录和子目录的名称/绝对路径，不读取文件内容，最多返回 200 个子目录并按名称排序。

- [ ] **Step 4: 写会话创建复用与消息分页测试**

```go
func TestCreateSessionReusesWorkspacePath(t *testing.T) {
    svc, db := newSessionServiceForTest(t)
    dir := t.TempDir()
    first, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID, Path: dir})
    require.NoError(t, err)
    second, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID, Path: dir})
    require.NoError(t, err)
    require.Equal(t, first.Workspace.ID, second.Workspace.ID)
    require.NotEqual(t, first.ID, second.ID)
    var count int64
    require.NoError(t, db.Model(&persistence.Workspace{}).Count(&count).Error)
    require.Equal(t, int64(1), count)
}
```

再覆盖空路径自动创建、DB 失败仅清理新空目录、搜索标题/路径、按 `session_seq` 分页，以及同路径其他会话存在活跃 Run 时 `workspace_warning=true`。

- [ ] **Step 5: 实现会话事务与 API**

`Create` 先生成 Session UUID 并准备工作区，再在事务中按规范化路径锁定或创建 Workspace，最后插入 Session。`Messages` 使用 `(session_id, session_seq)` 索引，`after` 默认为 0，`limit` 默认 100、最大 200。`List` 按 `last_active_at DESC` 排序，并支持可选 `workspace_path=<绝对路径>` 与 `active_run=true` 过滤，供新建会话界面在选择目录后查询并发警告。Run 活跃冲突检查通过 workspaces→sessions→runs join 完成，并排除当前会话。

- [ ] **Step 6: 验证工作区与会话测试**

Run: `go test ./internal/workspace ./internal/session ./internal/api -v`
Expected: PASS。

---

### Task 6: 持久化事件、SSE 回放与实时补洞

**Files:**
- Create: `internal/event/types.go`、`internal/event/store.go`、`internal/event/hub.go`
- Create: `internal/event/store_test.go`、`internal/event/store_integration_test.go`、`internal/api/sse.go`、`internal/api/sse_test.go`
- Modify: `internal/api/router.go`
- Test: `internal/event/store_test.go`、`internal/event/store_integration_test.go`、`internal/api/sse_test.go`

**Interfaces:**
- Produces: 核心接口中的 `event.Publisher`；`Event{ID,RunID,Seq,Type,Payload,CreatedAt}`；`api.SSEHandler(store, runFinder)`。

- [ ] **Step 1: 写事件序号和先持久后发布测试**

```go
func TestAppendIncrementsSequenceBeforePublish(t *testing.T) {
    db := openTestDB(t)
    runID := insertRunFixture(t, db)
    hub := NewHub()
    store := NewStore(db, hub)
    events, cancel := store.Subscribe(runID)
    defer cancel()
    first, err := store.Append(context.Background(), runID, "run.started", map[string]any{"ok": true})
    require.NoError(t, err)
    second, err := store.Append(context.Background(), runID, "message.started", map[string]any{"id": "m1"})
    require.NoError(t, err)
    require.Equal(t, int64(1), first.Seq)
    require.Equal(t, int64(2), second.Seq)
    var count int64
    require.NoError(t, db.Model(&persistence.RunEvent{}).Where("run_id = ?", runID).Count(&count).Error)
    require.Equal(t, int64(2), count)
    require.Equal(t, first.Seq, (<-events).Seq)
    require.Equal(t, second.Seq, (<-events).Seq)
}
```

并发启动 20 个 Append，断言序号为 1–20 且数据库无重复。

- [ ] **Step 2: 运行并确认失败**

Run: `go test -tags=integration ./internal/event -run TestAppend -v`
Expected: FAIL，`Store` 未定义。

- [ ] **Step 3: 实现事务事件存储与 Hub**

`Append` 在事务内 `SELECT next_event_seq FROM runs WHERE id=? FOR UPDATE`，用当前值插入事件，再把 `next_event_seq` 加一；提交成功后调用 `hub.Publish`。Hub 以 runID 管理 buffered channel，发布使用非阻塞发送；慢订阅者被关闭并移除，客户端用最后序号重连。取消订阅与发布通过同一把读写锁避免向已关闭 channel 发送。

- [ ] **Step 4: 写“先订阅再回放”和乱序发布补洞测试**

构造 Hub，将序号 3 在序号 2 发布前送入实时 channel；回放初始只有序号 1。SSE 发送器应输出 1、2、3，不重复，并在实时序号出现缺口时从 PostgreSQL 查询 `[last+1, incomingSeq]`。

```go
func TestOrderedStreamFillsLiveGap(t *testing.T) {
    fixture := newSSEFixture(t)
    fixture.persistEvents(1, 2, 3)
    live := make(chan event.Event, 3)
    live <- fixture.event(3)
    live <- fixture.event(2)
    out := collectOrderedEvents(t, fixture.store, live, 0, 3)
    require.Equal(t, []int64{1, 2, 3}, eventSequences(out))
}
```

- [ ] **Step 5: 实现 SSE 协议与有序发送器**

连接顺序固定为：验证 Run 存在→注册临时订阅→读取 `after` 与 `Last-Event-ID` 的较大值→按序回放→消费实时事件。实时序号 `<=last` 丢弃；`==last+1` 发送；`>last+1` 先查询并发送缺失事件。响应仅使用默认 SSE `message` 行：

```go
fmt.Fprintf(c.Writer, "id: %d\ndata: %s\n\n", item.Seq, data)
c.Writer.Flush()
```

响应头为 `Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`；每 15 秒发送 `: heartbeat\n\n`。客户端断开时取消订阅并返回，不把断开错误写入 Run。

- [ ] **Step 6: 验证事件与 SSE**

```bash
go test ./internal/event ./internal/api -v
go test -tags=integration ./internal/event -v
```

Expected: 并发序号、回放、去重、补洞与慢客户端测试 PASS。

---

### Task 7: Run 状态机、并发、取消与启动恢复

**Files:**
- Create: `internal/run/types.go`、`internal/run/store.go`、`internal/run/manager.go`、`internal/run/handler.go`
- Create: `internal/run/manager_test.go`、`internal/run/store_integration_test.go`
- Modify: `internal/api/router.go`、`cmd/euler/main.go`
- Test: `internal/run/manager_test.go`、`internal/run/store_integration_test.go`

**Interfaces:**
- Consumes: `event.Publisher`、`session.Service`、`modelprofile.Resolver`。
- Produces: `Manager.Start(ctx, sessionID, content) (Snapshot, error)`、`Manager.Cancel(ctx, runID) error`、`Manager.RecoverInterrupted(ctx) error`、`Manager.Shutdown(ctx) error`；核心接口中的 `run.Runner`。

- [ ] **Step 1: 写状态机测试**

```go
func TestRunTransitions(t *testing.T) {
    require.True(t, CanTransition("queued", "running"))
    require.True(t, CanTransition("running", "waiting_approval"))
    require.True(t, CanTransition("waiting_approval", "running"))
    require.True(t, CanTransition("running", "succeeded"))
    require.False(t, CanTransition("succeeded", "running"))
    require.False(t, CanTransition("cancelled", "waiting_approval"))
    require.False(t, CanTransition("queued", "interrupted"))
}
```

`interrupted` 只能由 `RecoverInterrupted` 的数据库修复路径设置，普通 `Transition` 不开放该目标。

- [ ] **Step 2: 写同会话冲突、跨会话并行与取消测试**

```go
func TestManagerRejectsSecondActiveRun(t *testing.T) {
    fixture := newManagerFixture(t)
    first, err := fixture.manager.Start(context.Background(), fixture.sessionID, "first")
    require.NoError(t, err)
    _, err = fixture.manager.Start(context.Background(), fixture.sessionID, "second")
    require.ErrorIs(t, err, ErrActiveRun)
    require.NoError(t, fixture.manager.Cancel(context.Background(), first.Run.ID))
}

func TestDifferentSessionsRunConcurrently(t *testing.T) {
    fixture := newManagerFixture(t)
    fixture.runner.block = true
    _, err := fixture.manager.Start(context.Background(), fixture.sessionA, "a")
    require.NoError(t, err)
    _, err = fixture.manager.Start(context.Background(), fixture.sessionB, "b")
    require.NoError(t, err)
    require.Eventually(t, func() bool { return fixture.runner.ActiveCount() == 2 }, time.Second, 10*time.Millisecond)
}
```

- [ ] **Step 3: 运行并确认失败**

Run: `go test ./internal/run -run 'TestRunTransitions|TestManager' -v`
Expected: FAIL，状态机与 Manager 未定义。

- [ ] **Step 4: 实现 Store 与 Manager**

`Start` 在一个事务内锁定 Session、确认没有活跃 Run、创建 Run（queued、next_event_seq=1）、写入带 runID 的用户消息和 streaming assistant message，并分别递增 `session_seq`；唯一索引冲突映射为 `ErrActiveRun`。数据库健康检查失败时不写入任何消息。Start 返回的 `run.Request` 同时携带 `AssistantMessageID`。

Manager 为每个 Run 创建带 `MaxDuration` 的 context，保存 cancel，并启动 goroutine。worker 依次执行：queued→running、发布 `run.started` 与 `message.started`、调用 `Runner.Run`、按结果进入 succeeded/failed/cancelled。终态更新 streaming message、取消 pending approvals、更新工具状态、写完成时间和终态事件，最后删除 cancel。

`Cancel` 先从 map 调用 cancel，再把仍处于活跃状态的 Run 置为 cancelled，取消 streaming message、pending approval 和运行中 tool call，发布 `run.cancelled`。重复取消返回最新快照，不重复发事件。

- [ ] **Step 5: 写并实现启动恢复**

集成测试先手工插入 running Run、streaming message、pending approval 和 running tool call，再调用 `RecoverInterrupted`：Run 与 message 必须为 `interrupted`，approval/tool call 为 `cancelled`，追加 `run.interrupted`，文件不发生回滚。启动流程必须在监听 HTTP 前完成恢复。

- [ ] **Step 6: 接入 Run API 并验证**

实现 `POST /sessions/{id}/runs`、`GET /runs/{id}`、`POST /runs/{id}/cancel`。Start 返回 `202`、Run、用户消息、streaming assistant message、`last_event_seq` 和 `workspace_warning`；同会话冲突返回 `409 active_run_exists`。Snapshot 聚合 Run、ToolCalls、Approvals 和 `next_event_seq-1`。

```bash
go test ./internal/run ./internal/api -v
go test -tags=integration ./internal/run -v
```

Expected: 状态机、并发、取消和恢复测试 PASS。

---

### Task 8: 安全文件工具与工作区产物 API

**Files:**
- Create: `internal/workspace/fs.go`、`internal/workspace/fs_test.go`、`internal/workspace/handler.go`
- Modify: `internal/api/router.go`
- Test: `internal/workspace/fs_test.go`

**Interfaces:**
- Consumes: `workspace.Service`。
- Produces: `FileStore.List/Read/Search/PrepareWrite/PreparePatch/Delete`；`FileChange{Diff string, Apply func(context.Context) error}`；文件树与预览 API。写操作只由 Task 10 的 Eino 工具在 Task 9 审批通过后调用。

- [ ] **Step 1: 写逃逸与符号链接失败测试**

```go
func TestFileStoreRejectsEscape(t *testing.T) {
    root := t.TempDir()
    outside := filepath.Join(t.TempDir(), "secret.txt")
    require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
    store := NewFileStore(root, 64*1024)
    _, err := store.Read(context.Background(), "../secret.txt")
    require.ErrorIs(t, err, ErrOutsideWorkspace)
    require.NoError(t, os.Symlink(outside, filepath.Join(root, "link.txt")))
    _, err = store.Read(context.Background(), "link.txt")
    require.ErrorIs(t, err, ErrOutsideWorkspace)
    _, err = store.Read(context.Background(), "/etc/passwd")
    require.ErrorIs(t, err, ErrOutsideWorkspace)
}
```

再覆盖不存在父目录、目录当文件读取、超过预览大小、UTF-8 文本、图片 MIME 和下载响应。

- [ ] **Step 2: 运行并确认失败**

Run: `go test ./internal/workspace -run TestFileStore -v`
Expected: FAIL，`FileStore` 未定义。

- [ ] **Step 3: 实现安全文件访问**

所有工具路径只接受工作区内相对路径，先拒绝 `filepath.IsAbs` 和含 `..` 的清理路径，再调用 `securejoin.SecureJoin(root, requested)`。对结果再次计算相对路径并用 `filepath.Rel` 验证不逃逸；读写前重新解析符号链接。读取使用带限制的 `io.LimitedReader`，超出限制返回明确错误，不把部分文件伪装成完整内容。

`PrepareWrite` 和 `PreparePatch` 只计算变更，不写文件。patch 参数为 `path`、`old_string`、`new_string`，要求旧字符串在文件中恰好出现一次。diff 使用 `difflib.UnifiedDiff`，`Apply` 执行时再次校验路径，并以临时文件+rename 写入，权限为 `0600`。Delete 只允许普通文件，返回目标路径供审批。

- [ ] **Step 4: 实现文件树、预览与下载 API**

`GET /workspaces/{id}/files` 返回最多 1000 个节点的相对路径、类型、大小和修改时间；遍历中遇到符号链接不跟随。`GET /workspaces/{id}/files/content?path=...&download=true` 复用同一个 FileStore 路径校验，按 `http.DetectContentType` 与扩展名设置 MIME；下载使用 `Content-Disposition: attachment`，预览限制使用 `Config.PreviewBytes`。

- [ ] **Step 5: 验证文件边界**

Run: `go test ./internal/workspace ./internal/api -v`
Expected: 路径逃逸、写入/补丁预计算无副作用和预览测试 PASS。

---

### Task 9: 审批服务与宿主机 Shell

**Files:**
- Create: `internal/approval/service.go`、`internal/approval/service_test.go`、`internal/approval/handler.go`
- Create: `internal/tools/shell.go`、`internal/tools/shell_test.go`、`internal/tools/process_darwin.go`、`internal/tools/process_other.go`
- Modify: `internal/run/store.go`、`internal/api/router.go`
- Test: `internal/approval/service_test.go`、`internal/tools/shell_test.go`

**Interfaces:**
- Consumes: `event.Publisher` 和 Run 状态更新。
- Produces: 核心接口中的 `approval.Gate`；`Shell.Execute(ctx, ShellRequest) ShellResult`；`SanitizeEnvironment(environ, secretNames []string) []string`。

- [ ] **Step 1: 写审批阻塞、拒绝与重复决策测试**

```go
func TestApprovalBlocksUntilDecision(t *testing.T) {
    fixture := newApprovalFixture(t)
    type requireResult struct {
        decision Decision
        err error
    }
    result := make(chan requireResult, 1)
    request := Request{RunID: fixture.runID, ToolCallID: fixture.toolCallID, Name: "shell", Payload: map[string]any{"command": "pwd"}}
    go func() {
        decision, err := fixture.service.Require(context.Background(), request)
        result <- requireResult{decision: decision, err: err}
    }()
    approvalID := fixture.waitForPendingApproval(t, fixture.toolCallID)
    require.Never(t, func() bool { return len(result) > 0 }, 100*time.Millisecond, 10*time.Millisecond)
    require.NoError(t, fixture.service.Decide(context.Background(), approvalID, true, "ok"))
    got := <-result
    require.NoError(t, got.err)
    require.True(t, got.decision.Approved)
    require.ErrorIs(t, fixture.service.Decide(context.Background(), approvalID, false, "again"), ErrAlreadyDecided)
}
```

`waitForPendingApproval` 只轮询测试 repository，待 Require 持久化 pending 行后返回其真实 ID；不得在 goroutine 中调用 `require`。Require 创建 pending approval、发布 `approval.requested` 并把 Run 置为 waiting_approval；批准恢复 running，拒绝保持 running并返回 `Approved=false`。context 取消时审批和 tool call 置 cancelled。

- [ ] **Step 2: 运行并确认失败**

Run: `go test ./internal/approval -v`
Expected: FAIL，审批服务未定义。

- [ ] **Step 3: 实现审批等待与 API**

Service 用 `map[approvalID]chan Decision` 管理本进程等待者，所有状态先写数据库再通知 channel。`POST /approvals/{id}/decision` 请求为 `{"approved":false,"reason":"当前目录不允许执行该命令"}`，已决策返回 `409 approval_already_decided`。决策后发布 `approval.resolved`。Run 取消复用相同的状态更新，保证等待者被唤醒。

- [ ] **Step 4: 写 Shell 环境、超时、输出限制与进程组测试**

Shell 的跨任务类型固定为：

```go
type ShellConfig struct {
    Timeout time.Duration
    MaxOutputBytes int
}
type ShellRequest struct {
    Dir string
    Command string
    Timeout time.Duration
    SecretNames []string
}
type ShellResult struct {
    Output string
    ExitCode int
    TimedOut bool
    Truncated bool
    Err error
}
type Shell struct { config ShellConfig }
func NewShellExecutor(config ShellConfig) *Shell { return &Shell{config: config} }
```

```go
func TestSanitizeEnvironmentRemovesModelSecrets(t *testing.T) {
    got := SanitizeEnvironment([]string{"PATH=/bin", "OPENAI_API_KEY=secret", "HOME=/tmp"}, []string{"OPENAI_API_KEY"})
    require.Equal(t, []string{"PATH=/bin", "HOME=/tmp"}, got)
}

func TestShellTimeoutAndTruncation(t *testing.T) {
    executor := NewShellExecutor(ShellConfig{Timeout: 50 * time.Millisecond, MaxOutputBytes: 8})
    result := executor.Execute(context.Background(), ShellRequest{Dir: t.TempDir(), Command: "printf 123456789"})
    require.True(t, result.Truncated)
    require.Equal(t, "12345678", result.Output)
    result = executor.Execute(context.Background(), ShellRequest{Dir: t.TempDir(), Command: "sleep 5"})
    require.ErrorIs(t, result.Err, context.DeadlineExceeded)
}
```

进程组测试运行 `sleep 30 & echo $! > child.pid; wait`，取消父命令后读取 child.pid，并用 `syscall.Kill(pid, 0)` 验证子进程已终止。拒绝审批的 Shell 测试使用会创建文件的命令，断言文件不存在。

- [ ] **Step 5: 实现 Shell 与进程组适配**

使用当前 `$SHELL`（空则 `/bin/zsh`）和 `exec.CommandContext(ctx, shell, "-lc", command)`，Dir 为工作区。`process_darwin.go` 设置 `Setpgid=true`，把 `Cmd.Cancel` 改为向负 PID 发送 SIGKILL，并设置 `WaitDelay`。`process_other.go` 返回 `ErrUnsupportedPlatform`，保留独立适配边界。

stdout/stderr 共用一个线程安全 LimitedBuffer，达到 `MaxOutputBytes` 后丢弃新增字节并设置 `Truncated`。结果区分 ExitCode、TimedOut、Truncated 和 Err；非零退出码作为结构化工具观察返回 Agent，不自动重试。

- [ ] **Step 6: 验证审批与 Shell**

Run: `go test ./internal/approval ./internal/tools ./internal/api -v`
Expected: 审批前后副作用、环境过滤、超时、截断和进程组取消测试 PASS。

---

### Task 10: Eino OpenAI Runtime、工具循环与流式落库

**Files:**
- Create: `internal/agent/model.go`、`internal/agent/runtime.go`、`internal/agent/stream.go`、`internal/agent/tools.go`
- Create: `internal/agent/model_test.go`、`internal/agent/runtime_test.go`、`internal/agent/openai_integration_test.go`
- Create: `internal/tools/file.go`、`internal/tools/file_test.go`、`internal/tools/shell_tool.go`
- Modify: `internal/tools/registry.go`、`internal/run/manager.go`、`cmd/euler/main.go`
- Test: `internal/agent/model_test.go`、`internal/agent/runtime_test.go`

**Interfaces:**
- Consumes: `run.Runner`、`modelprofile.Resolver`、`event.Publisher`、`approval.Gate`、文件与 Shell 工具。
- Produces: `agent.Runtime.Run(ctx, run.Request) error`；`OpenAIChatModelFactory.New`；Eino 工具注册表。

- [ ] **Step 1: 写 OpenAI 工厂与模型限轮失败测试**

```go
func TestRoundLimitedModelStopsExcessiveCalls(t *testing.T) {
    base := &fakeToolCallingModel{}
    limited := newRoundLimitedModel(base, 1)
    input := []*schema.Message{schema.UserMessage("hello")}
    _, err := limited.Generate(context.Background(), input)
    require.NoError(t, err)
    _, err = limited.Generate(context.Background(), input)
    require.ErrorIs(t, err, ErrModelRoundLimit)
    require.Equal(t, 1, base.generateCalls)
}
```

`WithTools` 必须调用底层 `WithTools` 并返回共享同一个计数器的新包装器；Stream 与 Generate 都计数。

- [ ] **Step 2: 写文本先于工具调用的 ReAct 流测试**

fake model 第一轮返回内容块 `"先检查"`，再返回带 `file_read` 的 ToolCall；第二轮返回 `"完成"`。Runtime 使用自定义 `StreamToolCallChecker` 消费检查分支的整个流，发现任意 chunk 含 ToolCalls 即返回 true。断言工具执行一次，事件包含 assistant 文本增量、tool.started/tool.completed、最终文本，且每个 StreamReader 都关闭。

- [ ] **Step 3: 运行并确认失败**

Run: `go test ./internal/agent -run 'TestRound|TestRuntime' -v`
Expected: FAIL，Runtime 与包装器未定义。

- [ ] **Step 4: 实现 OpenAI 工厂、错误映射和有限重试**

```go
func (OpenAIChatModelFactory) New(ctx context.Context, profile modelprofile.ResolvedProfile) (model.ToolCallingChatModel, error) {
    chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
        APIKey: profile.APIKey,
        BaseURL: profile.BaseURL,
        Model: profile.Model,
        Timeout: 60 * time.Second,
    })
    if err != nil { return nil, fmt.Errorf("create openai model: %w", err) }
    return chatModel, nil
}
```

在工厂外叠加 retry wrapper：仅对 `Generate`/`Stream` 初始调用中的网络错误、429 和 5xx 重试两次，退避 100ms/200ms；401/403/协议错误不重试，流开始后的错误不重试。错误映射为 `model_auth`、`model_rate_limited`、`model_unavailable`、`model_protocol`，写入 Run 前调用 `logging.Redact(err, profile.APIKey)`。

- [ ] **Step 5: 实现工具注册与结构化观察**

通过 `utils.InferTool` 注册 `file_list`、`file_read`、`file_search`、`file_write`、`file_patch`、`file_delete` 和 `shell`。先在 `file_test.go` 写工具级测试：read/list/search 直接返回 JSON；write/patch/delete 在 Gate 尚未批准时文件不变，批准后产生 diff 所述变更，拒绝返回 `{"status":"rejected"}` 而不是 Run 错误。

每个 Run 创建独立工具闭包，不共享工作区或审批状态。Shell 调用前通过 `Resolver.EnvironmentSecretNames` 读取全部模型配置的环境变量引用，放入 `ShellRequest.SecretNames`。工具执行顺序固定为：

1. 用 `compose.GetToolCallID(ctx)` 获取模型调用 ID，创建 ToolCall。
2. 检查 Run 级工具调用预算，超限返回 `ErrToolCallLimit`。
3. 发布 `tool.started`；敏感工具先生成 diff/命令载荷并调用 Gate。
4. 未批准返回 `{"status":"rejected","message":"用户拒绝"}`，Run 不失败。
5. 批准后执行并持久化结果，发布 completed/failed/progress。
6. 可恢复错误返回 `{"status":"failed","error":{"code":"file_not_found","message":"文件不存在：note.txt"}}`，让 Agent 修正；context 取消和预算错误才中止 Run。

所有参数、结果和错误在入库前用当前 Profile APIKey 脱敏，并按 `Agent.MaxOutputBytes` 截断发送给模型的观察。ReAct 配置使用 `ExecuteSequentially:true`，避免同一模型轮次内多个审批造成 Run 状态回退。

- [ ] **Step 6: 实现 MessageFuture 并发消费**

```go
option, future := react.WithMessageFuture()
agentInstance, err := react.NewAgent(ctx, &react.AgentConfig{
    ToolCallingModel: limitedModel,
    ToolsConfig: compose.ToolsNodeConfig{Tools: registry, ExecuteSequentially: true},
    StreamToolCallChecker: anyChunkToolCallChecker,
    MaxStep: limits.MaxModelRounds*2 + 10,
})
if err != nil { return err }
finalStream, err := agentInstance.Stream(ctx, history, option)
if err != nil { return err }
```

启动两个 goroutine：一个只排空 `finalStream` 并忽略内容，另一个遍历 `future.GetMessageStreams()`。future 中 assistant chunk 的 `Content` 批量写入 streaming message 并发布 `message.delta`，tool role 不重复写成 assistant 文本。批量阈值为 128 个 UTF-8 字符或当前模型流结束；flush 先更新消息再 Append 事件。两路都通过 `errgroup.WithContext` 传播错误，所有 reader `defer Close()`，不展示 `ReasoningContent`。

历史输入以一条 system message 开头，明确工作区路径、文件工具边界、敏感操作审批和简洁最终回答要求；随后只包含此前 completed 的 user/assistant 展示消息和当前 user 消息。当前 Run 的 tool call/result 由 Eino state 管理，不把隐式思维链写入历史。

- [ ] **Step 7: 用模拟 OpenAI HTTP 服务验证真实组件**

`openai_integration_test.go` 使用 `httptest.Server` 返回 OpenAI `chat/completions` SSE：第一响应带完整 tool_call，第二响应分块返回最终文本。通过真实 `openai.NewChatModel` 跑 Runtime，断言多轮请求中的 tool response、文本流、取消和异常映射，不访问外部网络。

- [ ] **Step 8: 验证 Agent 与竞态**

```bash
go test ./internal/agent ./internal/run ./internal/tools -v
go test -race ./internal/agent ./internal/run ./internal/event ./internal/approval ./internal/tools
```

Expected: 文本流、工具循环、审批拒绝、轮次/工具预算、取消和 race 测试 PASS。

---

### Task 11: React 基础、模型设置、会话与工作区选择

**Files:**
- Create: `web/package.json`、`web/vite.config.ts`、`web/tsconfig.json`、`web/index.html`
- Create: `web/src/main.tsx`、`web/src/App.tsx`、`web/src/api/client.ts`、`web/src/api/types.ts`
- Create: `web/src/features/settings/ModelProfiles.tsx`、`web/src/features/sessions/SessionSidebar.tsx`、`web/src/features/sessions/NewSessionDialog.tsx`
- Create: `web/src/features/sessions/NewSessionDialog.test.tsx`、`web/src/test/setup.ts`
- Modify: `Makefile`

**Interfaces:**
- Consumes: Task 4、5 的 REST DTO。
- Produces: `apiFetch<T>(path, init?)`、`useSessionList`、`useModelProfiles`、应用路由 `/`、`/sessions/:id`、`/settings`。

- [ ] **Step 1: 创建前端工程并安装依赖**

```bash
npm create vite@latest web -- --template react-ts
npm install --prefix web antd @tanstack/react-query zustand react-router-dom
npm install --prefix web react-markdown remark-gfm rehype-highlight highlight.js
npm install --prefix web -D vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event @playwright/test
```

Vite 监听 `127.0.0.1:5173`，proxy 将 `/api` 和 `/health` 转发到 `http://127.0.0.1:8080`，`changeOrigin:false`、`ws:false`。后端开发配置将 `http://127.0.0.1:5173` 加入 `allowed_origins`。

- [ ] **Step 2: 写新建会话失败测试**

```tsx
it('留空目录时明确提示自动创建工作区并提交空路径', async () => {
    const onCreate = vi.fn().mockResolvedValue({ id: 'session-1' })
    renderWithProviders(<NewSessionDialog open profiles={[profile]} onCreate={onCreate} />)
    expect(screen.getByText(/将自动创建默认工作区/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({ path: '' })))
})

it('已有目录存在活跃 Run 时展示并发修改警告', async () => {
    renderWithProviders(<NewSessionDialog open profiles={[profile]} workspaceWarning />)
    expect(screen.getByText(/其他会话正在此目录执行任务/)).toBeVisible()
})
```

- [ ] **Step 3: 运行并确认失败**

Run: `npm --prefix web test -- --run src/features/sessions/NewSessionDialog.test.tsx`
Expected: FAIL，组件尚不存在。

- [ ] **Step 4: 实现 API client、应用布局与查询**

`apiFetch` 使用 `credentials:'same-origin'` 和 `Content-Type:application/json`，首次收到 401 时 GET `/api/v1/bootstrap` 后仅重试一次；非 2xx 将后端 `code/message` 抛为 `APIError`。QueryClient 默认不重试写请求，GET 重试一次。

Ant Design `ConfigProvider` 使用中文 locale；路由由左侧 SessionSidebar、中间 Outlet、可折叠右侧文件区组成。设置页实现模型列表与创建表单，API Key 使用 Password 且表单说明“只写入，不回显”；环境变量模式不显示 APIKey 输入。

- [ ] **Step 5: 实现目录选择与会话页面**

NewSessionDialog 提供模型选择、绝对路径输入、目录浏览树和空路径提示。目录浏览器只调用 `/fs/directories`，不读文件。手动路径或目录选择变化后，以 `GET /sessions?workspace_path=<path>&active_run=true` 查询并展示并发警告；创建响应中的 `workspace_warning` 也传递到会话顶部。创建成功跳转 `/sessions/:id` 并使 sessions 查询失效。SessionSidebar 支持标题/路径搜索、最近活跃排序、Run 状态和 workspace basename，选中路径在刷新后保持。

- [ ] **Step 6: 验证前端**

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: Vitest 与 TypeScript/Vite build PASS。

---

### Task 12: 对话、SSE 恢复、工具时间线与审批交互

**Files:**
- Create: `web/src/stores/run.ts`、`web/src/stores/run.test.ts`
- Create: `web/src/features/chat/useRunEvents.ts`、`web/src/features/chat/ChatPanel.tsx`
- Create: `web/src/features/chat/MessageList.tsx`、`web/src/features/chat/ToolTimeline.tsx`、`web/src/features/chat/ApprovalCard.tsx`
- Create: `web/src/features/chat/ApprovalCard.test.tsx`、`web/src/features/chat/ChatPanel.test.tsx`
- Modify: `web/src/App.tsx`、`web/src/api/types.ts`

**Interfaces:**
- Consumes: Run API、SSE Envelope、Approval API。
- Produces: `useRunEvents(snapshot)`、`useRunStore.applyEvent`、`ChatPanel`。

- [ ] **Step 1: 写 SSE 去重、重连和草稿测试**

```ts
it('按 run_id + seq 去重并只追加一次文本', () => {
    const store = createRunStore()
    const envelope = event({ run_id: 'r1', seq: 7, type: 'message.delta', payload: { message_id: 'm1', delta: '你好' } })
    store.getState().applyEvent(envelope)
    store.getState().applyEvent(envelope)
    expect(store.getState().deltas.m1).toBe('你好')
    expect(store.getState().lastSeq.r1).toBe(7)
})

it('Run 活跃时保留草稿但不创建第二个 Run', () => {
    const store = createRunStore()
    store.getState().setDraft('s1', '下一条消息')
    store.getState().setActive('s1', 'r1')
    expect(store.getState().drafts.s1).toBe('下一条消息')
    expect(store.getState().activeBySession.s1).toBe('r1')
})
```

- [ ] **Step 2: 运行并确认失败**

Run: `npm --prefix web test -- --run src/stores/run.test.ts`
Expected: FAIL，Zustand store 未定义。

- [ ] **Step 3: 实现 Zustand 与 EventSource 生命周期**

Zustand 仅保存 `deltas`、`lastSeq`、`seenEventKeys`、连接状态、active run、草稿和面板状态。`useRunEvents` 从 Snapshot 的 `last_event_seq` 建立 `/runs/{id}/events?after=N`，EventSource 自动重连时服务端同时使用 Last-Event-ID。收到事件先检查 `${run_id}:${seq}`，再按类型更新增量或失效 messages/run/sessions/files 查询；终态关闭连接并清空本地 deltas，让权威快照接管。

React StrictMode 下 effect cleanup 必须关闭旧 EventSource；组件卸载、切换会话和 Run 终态都取消连接。连接状态显示“连接中/已连接/重连中”，不根据 onerror 把 Run 误判为 failed。

- [ ] **Step 4: 写审批与消息组件测试**

ApprovalCard 覆盖文件 diff、完整 Shell 命令/目录/超时、批准、拒绝、重复点击禁用和 409 后刷新 Snapshot。MessageList 使用 react-markdown+GFM+highlight.js，代码块提供复制；只渲染 `content`，不读取或展示 reasoning 字段。

```tsx
it('Shell 审批显示完整命令并允许拒绝', async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined)
    render(<ApprovalCard approval={shellApproval} onDecide={onDecide} />)
    expect(screen.getByText('rm ./generated.tmp')).toBeVisible()
    expect(screen.getByText(/可访问当前 OS 用户有权限的其他资源/)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '拒绝' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(shellApproval.id, false, ''))
})
```

- [ ] **Step 5: 实现 ChatPanel 和工具时间线**

顶部显示工作区绝对路径、模型名、Run 状态和并发警告；中间按消息序号展示 Markdown，工具卡片展示参数摘要、状态、结果摘要和错误。输入区支持 Enter 发送、Shift+Enter 换行；活跃 Run 禁用发送并显示停止按钮，草稿持续保存。停止需二次确认，取消后立即重新获取 Run Snapshot。

审批按钮调用 `/approvals/{id}/decision`，等待期间禁止重复提交；拒绝可填写原因。拒绝后工具时间线显示 rejected，聊天继续等待 Agent 最终回答，不把整个 Run 标红为失败。

- [ ] **Step 6: 验证前端对话**

Run: `npm --prefix web test -- --run && npm --prefix web run build`
Expected: SSE、审批、Markdown、草稿和停止交互测试 PASS。

---

### Task 13: 产物预览、Go 静态托管与优雅退出

**Files:**
- Create: `web/src/features/files/FilePanel.tsx`、`web/src/features/files/FilePreview.tsx`、`web/src/features/files/FilePanel.test.tsx`
- Create: `web/embed.go`、`internal/api/static.go`、`internal/api/static_test.go`
- Modify: `internal/api/router.go`、`cmd/euler/main.go`、`Makefile`
- Test: `web/src/features/files/FilePanel.test.tsx`、`internal/api/static_test.go`

**Interfaces:**
- Consumes: Workspace 文件树与内容 API。
- Produces: 可折叠文件面板；Go 同源托管前端；HTTP graceful shutdown。

- [ ] **Step 1: 写文本、Markdown、图片与下载测试**

FilePanel 测试目录展开、选择文件、超出大小错误、图片使用受保护 API URL、Markdown 渲染和下载链接。不得使用 `file://`、绝对本机路径或未经后端校验的静态文件路径。

- [ ] **Step 2: 实现 FilePanel**

右侧 Tree 展示相对路径和修改状态；文件修改/Run 终态事件使 files 查询失效。预览按 MIME 与扩展名分流：文本用代码视图，`.md` 用消息相同 Markdown renderer，png/jpeg/gif/webp 用 img，其他类型显示下载按钮。下载链接指向同一个 content API 且 `download=true`。

- [ ] **Step 3: 写 Go SPA 回退测试**

```go
func TestStaticSPAFallback(t *testing.T) {
    router := newProductionRouter(t)
    for _, path := range []string{"/", "/sessions/abc", "/settings"} {
        response := httptest.NewRecorder()
        request := httptest.NewRequest(http.MethodGet, path, nil)
        router.ServeHTTP(response, request)
        require.Equal(t, http.StatusOK, response.Code)
        require.Contains(t, response.Body.String(), "<div id=\"root\"></div>")
    }
}
```

- [ ] **Step 4: 实现静态托管与优雅退出**

`web/embed.go` 使用 `//go:embed all:dist`，`Dist()` 返回 `fs.FS`。Gin 对 `/assets/*` 提供带缓存的静态资源，对非 `/api`、`/health` 的 GET 回退 index.html；未知 API 仍返回 JSON 404，不能回退 HTML。

main 在迁移和恢复后启动 `http.Server`，监听 SIGINT/SIGTERM，给 HTTP 10 秒退出时间，再调用 `Manager.Shutdown` 并关闭数据库。`Manager.Shutdown` 取消本进程 Run context、等待 worker 退出，将仍活跃的 Run 和 streaming message 标为 `interrupted`，取消待审批和运行中工具并写 `run.interrupted`；worker 不得随后覆盖为 cancelled。用户主动停止仍写 cancelled，进程退出写 interrupted。开发命令为 `make dev-api` 和 `make dev-web`；生产命令 `make build` 先 `npm --prefix web ci && npm --prefix web run build`，再 `go build -o bin/euler ./cmd/euler`。

- [ ] **Step 5: 验证前后端与同源页面**

```bash
npm --prefix web test -- --run
npm --prefix web run build
go test ./...
go build ./cmd/euler
```

Expected: 前端测试、Go 测试与构建 PASS，SPA 路由返回同源 HTML。

---

### Task 14: Playwright 端到端验收与全量质量检查

**Files:**
- Create: `web/playwright.config.ts`、`web/tests/mock-openai.mjs`、`web/tests/workbench.spec.ts`、`web/tests/euler-e2e.yaml`
- Create: `cmd/euler/restart_integration_test.go`
- Modify: `Makefile`、`web/package.json`
- Test: `web/tests/workbench.spec.ts`、`cmd/euler/restart_integration_test.go`

**Interfaces:**
- Consumes: 全部 REST/SSE、真实 Go 后端、PostgreSQL、模拟 OpenAI。
- Produces: 不依赖付费模型的 macOS 端到端验收命令，以及独立执行的真实供应商手动冒烟步骤。

- [ ] **Step 1: 编写模拟 OpenAI 服务**

Node HTTP 服务监听 `127.0.0.1:18081`，实现 `POST /v1/chat/completions`。第一次请求返回 OpenAI SSE ToolCall：`file_write(path="note.txt", content="# Euler 验收\n\n产物预览成功。")`；包含对应 tool role 的第二次请求返回分块文本“已创建 `note.txt`，可以在右侧预览。”；当用户消息为“持续输出”时持续发送文本 chunk，直到客户端断流，用于停止测试。响应协议与真实 OpenAI 一致，不引入 Eino mock 替代真实 HTTP 组件。

- [ ] **Step 2: 写端到端主流程测试**

```ts
test('自动工作区、流式对话、文件审批与产物预览', async ({ page }) => {
    await page.goto('/settings')
    await page.getByRole('button', { name: '新建模型配置' }).click()
    await page.getByLabel('名称').fill('本地模拟模型')
    await page.getByLabel('Base URL').fill('http://127.0.0.1:18081/v1')
    await page.getByLabel('模型').fill('euler-e2e')
    await page.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
    await page.getByRole('button', { name: '保存' }).click()

    await page.goto('/')
    await page.getByRole('button', { name: '新建会话' }).click()
    await expect(page.getByText(/将自动创建默认工作区/)).toBeVisible()
    await page.getByRole('button', { name: '创建会话' }).click()
    await page.getByPlaceholder('输入消息').fill('创建验收文档')
    await page.getByRole('button', { name: '发送' }).click()

    await expect(page.getByText('file_write')).toBeVisible()
    await expect(page.getByText('note.txt')).toBeVisible()
    await page.reload()
    await expect(page.getByRole('button', { name: '批准' })).toBeVisible()
    await page.getByRole('button', { name: '批准' }).click()
    await expect(page.getByText(/可以在右侧预览/)).toBeVisible()
    await page.getByText('note.txt', { exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Euler 验收' })).toBeVisible()
})
```

增加第二个用例：模型 slow 流期间刷新页面无重复文本，点击停止后状态为 cancelled，刷新后仍保持一致；进程级测试覆盖 interrupted，本用例只验证 cancelled。

- [ ] **Step 3: 配置真实服务编排**

Playwright `webServer` 启动 mock OpenAI 和 `go run ./cmd/euler -config web/tests/euler-e2e.yaml`，端口分别为 18081、18080；环境设置 `EULER_DATABASE_URL` 与 `EULER_TEST_MODEL_KEY=euler-not-a-real-secret`。Go `webServer.cwd` 通过 `path.resolve(__dirname, '..')` 固定为仓库根目录，使 `file://migrations` 始终可解析；mock 的 cwd 为 `web`，浏览器 `baseURL` 为 `http://127.0.0.1:18080`。E2E 配置监听同一地址，工作区根为 `/tmp/euler-e2e-workspaces`，允许 Origin `http://127.0.0.1:18080`。Playwright 使用 `workers:1`，每次运行前只清理 `euler_test` 的 public schema 和 `/tmp/euler-e2e-workspaces`，绝不清理开发数据库或 `~/EulerWorkspaces`。

- [ ] **Step 4: 写真实进程崩溃与重启集成测试**

`restart_integration_test.go` 使用 `//go:build integration`。fixture 构建真实 Euler 二进制，以独立临时配置、工作区、可用 loopback 端口和 Go `httptest.Server` 模拟 OpenAI 启动子进程；子进程 cwd 固定为仓库根目录，模型配置、会话和 Run 全部通过真实 REST API 创建。

```go
func TestProcessRestartMarksActiveRunInterrupted(t *testing.T) {
    fixture := newEulerProcessFixture(t)
    runID := fixture.startRunAwaitingFileApproval(t)
    fixture.killWithSIGKILL(t)
    fixture.restart(t)

    snapshot := fixture.runSnapshot(t, runID)
    require.Equal(t, "interrupted", snapshot.Run.Status)
    require.Equal(t, "interrupted", fixture.assistantMessage(t, runID).Status)
    require.Len(t, snapshot.ToolCalls, 1)
    require.Equal(t, "cancelled", snapshot.ToolCalls[0].Status)
    require.Len(t, snapshot.Approvals, 1)
    require.Equal(t, "cancelled", snapshot.Approvals[0].Status)
    require.NoFileExists(t, fixture.workspaceFile("note.txt"))
    require.Zero(t, fixture.modelRequestsSinceRestart())

    continuedRunID := fixture.startRun(t, "请基于完整历史显式继续")
    require.NotEqual(t, runID, continuedRunID)
}
```

`killWithSIGKILL` 不走优雅退出，专门验证下次启动的恢复路径；重启后、显式继续前模型请求数必须为零，证明没有自动重放文件工具或续跑模型流。fixture 的 helper 必须在测试内实现进程等待、HTTP 轮询和清理，不能用直接调用 `Manager.RecoverInterrupted` 替代真实重启。

- [ ] **Step 5: 执行端到端与进程重启测试并修复定位到的问题**

```bash
docker compose up -d postgres
docker compose exec -T postgres psql -U euler -d euler -c "SELECT 'CREATE DATABASE euler_test' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='euler_test')\gexec"
export EULER_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler_test?sslmode=disable'
export TEST_DATABASE_URL="$EULER_DATABASE_URL"
npm --prefix web exec -- playwright install chromium
npm --prefix web run test:e2e
go test -p=1 -tags=integration ./cmd/euler -run TestProcessRestart -v
```

Expected: 两个 Chromium 用例和真实进程重启用例 PASS；trace 中可看到等待审批刷新恢复、SSE 无重复和文件预览，重启后没有自动模型请求。

- [ ] **Step 6: 执行全量质量与自动验收检查**

```bash
export EULER_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler_test?sslmode=disable'
export TEST_DATABASE_URL="$EULER_DATABASE_URL"
go mod tidy
go test ./...
go test -p=1 -tags=integration ./...
go test -race ./internal/agent ./internal/run ./internal/event ./internal/approval ./internal/tools
golangci-lint run --issues-exit-code=1 --timeout=5m ./...
npm --prefix web test -- --run
npm --prefix web run build
npm --prefix web run test:e2e
```

人工对照规格验收：默认/已有工作区及复用、同会话 409 与跨会话并行、六类文件工具、Shell 批准/拒绝/超时/停止、文本/Markdown/图片/下载、重启 interrupted、API Key 不出现在数据库/事件/API/日志。任何未通过项都回到所属任务补失败测试和最小修复，不扩大第一阶段范围。

- [ ] **Step 7: 手动执行一次真实模型供应商冒烟**

此步骤不由 CI 或上述自动化命令触发。选择有权使用的 OpenAI 兼容供应商，在开发数据库 `euler` 中操作，使用一个专门为冒烟创建的空目录，避免接触 E2E 数据。先构建生产形态，再在当前终端隐藏输入真实 Key 并启动服务；Key 不写入配置文件、命令参数、文档或 shell 历史：

```bash
docker compose up -d postgres
export EULER_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable'
make build
read -s EULER_SMOKE_MODEL_KEY
export EULER_SMOKE_MODEL_KEY
./bin/euler -config "$HOME/.euler/config.yaml"
```

在设置页填写该供应商真实的 HTTPS Base URL 和模型 ID，密钥类型选择 environment、引用名为 `EULER_SMOKE_MODEL_KEY`。随后人工完成：

1. 在空目录创建会话，要求模型只通过 `file_write` 创建 `smoke.md`；确认流式文本、完整 diff 和审批阻塞，批准后检查文件内容和 Markdown 预览。
2. 发起新 Run，要求模型只通过 Shell 执行 `printf smoke > shell-smoke.txt`；拒绝所有该命令审批，确认文件不存在、工具为 rejected 且 Run 不因拒绝直接失败。
3. 刷新历史并检查模型配置、Run、事件和工具 API 响应及终端日志均不包含 Key；确认界面有云端供应商内容传输提示。
4. 停止服务并执行 `unset EULER_SMOKE_MODEL_KEY`。任何失败先补不访问真实供应商的自动化回归测试，再做最小修复；手动冒烟不能替代自动验收。

---

## 实施顺序与检查点

1. Tasks 1–5 完成后，本机安全配置、工作区和会话 REST 可用。
2. Tasks 6–9 完成后，持久化 Run/SSE、文件与 Shell 审批可独立测试。
3. Task 10 完成后，模拟模型可驱动完整 Agent 后端竖切。
4. Tasks 11–13 完成后，React 工作台与 Go 单进程同源应用可用。
5. Task 14 完成后，才可将第一阶段描述为已验证；测试未运行或未通过时不得宣称完成。
