# Euler

Euler 是一个本地优先的编码 Agent 工作台：由 Go 后端、React 控制台和一个可独立运行的一次性 Agent 二进制组成。它既能作为日常编码助手在本地仓库里读写文件、执行命令，也能把同一个 Agent 打包进任务容器，接入 Harbor 做标准化编码评测。

## 演示

![Euler 控制台演示：创建会话、观察工具时间线、完成一次 Go 编码任务](docs/assets/demo.gif)

上图是一次完整 run 的压缩预览（34 秒）：新建会话并输入任务、Agent 依次执行 `go version`、写入 `main.go` 与 `go.mod`、运行 `gofmt` / `go vet` / `go build` / `go run`，右侧工作区同步列出产物，最后给出任务完成总结。

- [查看高清视频（MP4，H.264 / yuv420p，浏览器与本地播放器均可直接打开）](docs/assets/demo.mp4)
- GIF 仅用于 README 内嵌预览，细节以 MP4 原片为准。

## 核心能力

- **ReAct 编码循环**：基于 Eino 的工具调用循环，支持多轮模型交互、流式输出与显式 `submit` 收尾。
- **可配置预算**：模型轮次、工具调用次数、运行时长、单次工具输出都可设置为硬上限，避免死循环和成本失控。
- **会话与运行管理**：每个会话同时只允许一个活跃 run，完整状态机（`queued` / `running` / `waiting_approval` / `succeeded` / `failed` / `cancelled` / `interrupted`），服务重启后可恢复中断状态。
- **实时事件流**：通过 SSE 推送 run 状态、消息增量与工具调用事件。
- **编码工具集**：`file_list`、`file_read`、`file_search`、`file_write`、`file_patch`、`shell`、`submit`。
- **工作区与文件浏览**：目录树、文件内容预览（带大小限制），run 结束后自动列出新增产物。
- **工作台界面**：工具时间线（命令输出、文件写入 diff、思考过程）、Markdown 与高亮渲染、历史 run 回放，内置石墨 / 纸白 / 青瓷 / 午夜霓虹四套主题。
- **模型配置**：模型 Profile 存 Postgres，API Key 存 macOS Keychain，不落库、不写日志；支持从 Base URL 拉取可用模型列表。
- **全链路追踪**：可选接入 Langfuse，记录每次模型调用与工具调用。
- **Harbor 评测**：`euler-agent` 二进制直接上传到任务容器内执行，不需要宿主机服务或工作区挂载；内置按难度分组的精选任务清单。

## 架构

```
┌──────────────────────────┐        ┌─────────────────────────────┐
│ web/  React + Vite 控制台 │──HTTP──▶│ cmd/euler  Gin API 服务      │
└──────────────────────────┘  SSE   └──────────────┬──────────────┘
                                                  │
                        ┌─────────────────────────┼─────────────────────────┐
                        ▼                         ▼                         ▼
                 run.Manager              agent.Runtime               持久化 / 追踪
            状态机、并发、取消、恢复      Eino ReAct + 工具 + 模型      Postgres / Langfuse
                                                  │
                                                  ▼
                                        file_* / shell / submit
```

评测链路与在线服务解耦：

```
harbor run ──▶ 任务镜像 ──▶ 上传 euler-agent ──▶ 容器内执行 ──▶ result.json + reward.txt
```

## 目录结构

| 路径 | 说明 |
|---|---|
| `cmd/euler` | HTTP 服务入口，加载配置、迁移数据库、挂载路由 |
| `cmd/euler-agent` | 无数据库的一次性 Agent 入口，供容器内评测使用 |
| `internal/agent` | ReAct 运行时、模型封装、提示词模板、预算控制 |
| `internal/tools` | 文件、shell、submit 等工具实现 |
| `internal/run` | run 状态机、按会话并发控制、取消与启动恢复 |
| `internal/session` | 会话与消息 |
| `internal/workspace` | 工作区与文件浏览 |
| `internal/modelprofile` | 模型 Profile 与 Keychain 密钥存储 |
| `internal/approval` | 审批服务（当前默认自动放行） |
| `internal/event` | 事件存储与 SSE 广播 |
| `internal/config` | 配置文件加载、默认值与校验 |
| `internal/persistence` | GORM 连接、迁移与健康检查 |
| `internal/logging` | 结构化日志 |
| `internal/api` | 路由、本机鉴权、静态资源 |
| `internal/tracing` | Langfuse 追踪 |
| `web/` | React 控制台（Vite + Ant Design + TanStack Query + Zustand） |
| `harbor_eval/` | Harbor Agent、任务清单、筛选脚本与运行结果 |
| `migrations/` | Postgres SQL 迁移 |
| `docs/` | 设计文档与实现计划 |
| `docs/assets/` | README 演示素材（`demo.mp4` 与内嵌预览 `demo.gif`） |

## 快速开始

### 依赖

- Go 1.26+
- Node.js 20+
- Docker（本机 Postgres，或使用 `compose.yaml`）
- `golangci-lint`（提交前检查）

### 1. 启动数据库

```bash
docker compose up -d postgres
```

默认连接串：`postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable`。

### 2. 配置

服务从 `~/.euler/config.yaml` 读取配置（不存在则使用默认值）。`make dev-api` 与
`make dev-restart` 默认使用与 `compose.yaml` 一致的数据库地址，无需手工导出；
需要指向其他库时用环境变量覆盖：

```bash
EULER_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/euler?sslmode=disable' make dev-api
```

可选追踪配置：

```bash
export EULER_LANGFUSE_HOST='https://cloud.langfuse.com'
export EULER_LANGFUSE_PUBLIC_KEY='pk-...'
export EULER_LANGFUSE_SECRET_KEY='sk-...'
```

`make dev-api` 会自动从 `~/.zprofile` 读取 `LANGFUSE_HOST`、`LANGFUSE_BASE_URL`、
`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY`，因此本地用 Web 界面调试时默认带上追踪；
未配置这些变量则不追踪。E2E 使用独立的配置文件与变量，不会上传追踪数据。
启动日志出现 `langfuse tracing enabled` 表示生效。

模型、Base URL 和 API Key 通过控制台的「模型配置」创建，密钥写入 macOS Keychain。

### 3. 启动

```bash
# 后端（默认 127.0.0.1:8080）
make dev-api

# 改完后端代码后重启后端
make dev-restart

# 前后端一起重启：后端 8080 + 前端开发服务器 5173
make dev-restart-all

# 改完前端代码后使用：重新构建嵌入产物再重启后端（不走 5173 时用这个）
make dev-reload

# 只启动前端开发服务器（127.0.0.1:5173，代理 /api 与 /health 到 8080，热更新）
make dev-web
```

生产构建会把前端产物嵌入 Go 二进制：

```bash
make build     # 构建 bin/euler
./bin/euler
```

### 4. 常用检查

```bash
make test      # go test ./...
make lint      # golangci-lint run --timeout=5m ./...
make web-test  # 前端单测
make test-e2e  # Playwright 端到端测试
```

## 配置参考

`~/.euler/config.yaml` 支持以下字段（均可省略）：

| 字段 | 默认值 | 说明 |
|---|---|---|
| `listen` | `127.0.0.1:8080` | 仅允许 loopback 地址 |
| `workspace_root` | `~/EulerWorkspaces` | 工作区根目录 |
| `allowed_origins` | 空 | 额外允许的 Origin |
| `agent.max_model_rounds` | `0` | 模型轮次上限，`0` 表示不限制 |
| `agent.max_tool_calls` | `0` | 工具调用次数上限，`0` 表示不限制 |
| `agent.max_duration` | `10m` | 单次 run 时长上限 |
| `agent.max_output_bytes` | `65536` | 输出上限 |
| `shell_timeout` | `2m` | 单条 shell 命令超时 |
| `shell_output_bytes` | `1MiB` | 单条命令输出上限 |
| `shell_path_prefix` | 空 | 注入到 PATH 最前的目录 |
| `model_timeout` | `120s` | 单次模型请求超时 |
| `model_reasoning_effort` | 空 | `low` / `medium` / `high` / `max` |
| `system_prompt_template` | `coding` | 提示词模板名 |
| `preview_bytes` | `1MiB` | 文件预览上限 |
| `langfuse.*` | 空 | 追踪配置，密钥只从环境变量读取 |

## HTTP API

所有接口位于 `/api/v1`（`/health` 除外），通过本机 cookie 与 Origin 校验保护：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 数据库健康检查 |
| GET | `/api/v1/bootstrap` | 初始化本机会话 cookie |
| GET/POST | `/api/v1/model-profiles` | 列出 / 创建模型配置 |
| POST | `/api/v1/model-profiles/models` | 按 Base URL 拉取可用模型列表 |
| PUT/DELETE | `/api/v1/model-profiles/:id` | 更新 / 删除模型配置 |
| GET | `/api/v1/fs/directories` | 浏览文件系统目录 |
| GET | `/api/v1/workspaces/:id/files` | 列出工作区文件 |
| GET | `/api/v1/workspaces/:id/files/content` | 读取文件内容 |
| POST/GET | `/api/v1/sessions` | 创建 / 列出会话 |
| GET/PATCH/DELETE | `/api/v1/sessions/:id` | 会话详情 / 重命名 / 删除 |
| GET | `/api/v1/sessions/:id/messages` | 会话消息 |
| POST | `/api/v1/sessions/:id/runs` | 启动一次 run |
| GET | `/api/v1/runs/:id` | run 详情 |
| POST | `/api/v1/runs/:id/cancel` | 取消 run |
| GET | `/api/v1/runs/:id/events` | SSE 事件流 |
| POST | `/api/v1/approvals/:id/decision` | 提交审批结果 |

## Harbor 编码评测

评测用的 `euler-agent` 是同一个 Agent 运行时的无数据库入口，被上传到任务容器内直接执行，工作区就是容器工作目录。

### 前置条件

- 已安装 `harbor` CLI 与 Docker
- 设置模型密钥：`EULER_MODEL_API_KEY` 或 `OPENAI_API_KEY`
- 可选：`EULER_MODEL`（默认 `deepseek-v4-flash`）、`EULER_MODEL_BASE_URL`、`EULER_REASONING_EFFORT`

### 运行方式

```bash
make agent-dist          # 交叉编译 linux/amd64 与 linux/arm64 的 agent 二进制

make harbor-test         # 默认快速回归清单
make harbor-test-easy    # 简单组（10 个任务）
make harbor-test-medium  # 中等组（10 个任务）
make harbor-test-hard    # 困难组（10 个任务）
make harbor-test-all     # 依次运行三组
```

任务清单位于：

- `harbor_eval/tasks/selected-tasks.txt`：默认快速回归集
- `harbor_eval/tasks/selected-easy.txt`
- `harbor_eval/tasks/selected-medium.txt`
- `harbor_eval/tasks/selected-hard.txt`

评测预算：

- 容器内 Agent 默认 **模型轮次与工具调用都不设限**（`-max-rounds 0`、`-max-tool-calls 0`），单次运行 600 秒
- 可用 `EULER_MAX_MODEL_ROUNDS`、`EULER_MAX_TOOL_CALLS`、`EULER_MAX_DURATION_SEC` 收紧预算；下文评测结果的工具调用上限为 120
- 单次模型请求超时 120 秒
- Harbor agent 阶段硬超时 780 秒（600s agent 预算 + 120s exec 余量 + 60s 结果回传余量）

运行结果写入 `harbor_eval/results/<run_id>/`，每个 trial 目录下包含 `agent/result.json`、`verifier/reward.txt` 与 `verifier/test-stdout.txt`。

### 评测结果

以下为 2026-11-21 在本机实际运行的结果：`deepseek-v4-flash` + `EULER_REASONING_EFFORT=high`，macOS arm64 Docker Desktop，并发 4，每个任务只采样 1 次（n=1）。`harbor_eval/results/` 已被 `.gitignore` 排除，结果目录不会进入仓库，因此在这里贴出汇总数据。

| 任务组 | 通过 | 通过率 | 平均耗时 | 中位耗时 | 时间窗口 |
|---|---|---|---|---|---|
| 简单组（easy） | **10/10** | 100% | 8.4s | 8.7s | 17:32 → 17:34 |
| 中等组（medium） | **9/10** | 90% | 13.9s | 9.4s | 17:34 → 17:36 |
| 困难组（hard） | **5/10** | 50% | 281.1s | 185.4s | 17:36 → 17:54 |
| 三组合计 | **24/30** | 80% | — | — | — |
| 默认快速回归集 | **9/10** | 90% | 30.4s | 9.8s | 21:15 → 21:19 |

#### 简单组（`selected-easy.txt`，10/10）

| 任务 | 结果 | 耗时 | 工具调用 |
|---|---|---|---|
| `humanevalfix/python-1` | ✅ | 10.1s | 7 |
| `humanevalfix/python-3` | ✅ | 6.6s | 5 |
| `humanevalfix/python-8` | ✅ | 9.0s | 7 |
| `humanevalfix/python-11` | ✅ | 6.1s | 5 |
| `humanevalfix/python-12` | ✅ | 5.5s | 5 |
| `humanevalfix/python-15` | ✅ | 11.6s | 9 |
| `humanevalfix/python-17` | ✅ | 6.5s | 5 |
| `humanevalfix/python-18` | ✅ | 8.4s | 6 |
| `humanevalfix/python-20` | ✅ | 9.6s | 5 |
| `humanevalfix/python-21` | ✅ | 10.2s | 8 |

#### 中等组（`selected-medium.txt`，9/10）

| 任务 | 结果 | 耗时 | 工具调用 | 备注 |
|---|---|---|---|---|
| `humanevalfix/python-0` | ✅ | 9.9s | 6 | |
| `humanevalfix/python-2` | ✅ | 8.9s | 6 | |
| `humanevalfix/python-4` | ✅ | 7.4s | 5 | |
| `humanevalfix/python-5` | ✅ | 9.0s | 6 | |
| `humanevalfix/python-6` | ✅ | 12.3s | 7 | |
| `humanevalfix/python-7` | ✅ | 8.0s | 6 | |
| `humanevalfix/python-9` | ✅ | 7.8s | 6 | |
| `humanevalfix/python-10` | ✅ | 12.6s | 9 | |
| `aider-polyglot/polyglot_python_affine-cipher` | ✅ | 40.9s | 8 | |
| `aider-polyglot/polyglot_python_beer-song` | ❌ | 22.5s | 9 | 该任务在其它批次中可通过，属模型波动 |

#### 困难组（`selected-hard.txt`，5/10）

| 任务 | 结果 | 耗时 | 工具调用 | 备注 |
|---|---|---|---|---|
| `tb21-systems-security/feal-linear-cryptanalysis` | ✅ | 193.2s | 30 | |
| `tb21-systems-security/fix-code-vulnerability` | ✅ | 29.7s | 18 | |
| `tb21-systems-security/configure-git-webserver` | ✅ | 158.7s | 25 | |
| `tb21-systems-security/password-recovery` | ✅ | 177.7s | 35 | |
| `tb21-code-debug/path-tracing-reverse` | ✅ | 329.5s | 31 | |
| `tb21-code-debug/gpt2-codegolf` | ❌ | 600.0s | 57 | 用满 600s 预算 |
| `tb21-data-science/llm-inference-batching-scheduler` | ❌ | 600.0s | 32 | 用满 600s 预算 |
| `tb21-code-debug/make-mips-interpreter` | ❌ | 600.1s | 78 | 用满 600s 预算 |
| `tb21-systems-security/model-extraction-relu-logits` | ❌ | 121.2s | 2 | 模型流超时（`model_timeout`） |
| `tb21-code-debug/write-compressor` | ❌ | 0.5s | 0 | 容器内 TLS 证书校验失败，模型不可用（`model_unavailable`） |

#### 默认快速回归集（`selected-tasks.txt`，9/10）

| 任务 | 结果 | 耗时 | 工具调用 |
|---|---|---|---|
| `terminal-bench/cancel-async-tasks` | ✅ | 74.5s | 13 |
| `terminal-bench/headless-terminal` | ✅ | 139.2s | 17 |
| `humanevalfix/python-0` ~ `python-5`（6 个） | ✅ | 7.4s ~ 10.1s | 4 ~ 8 |
| `aider-polyglot/polyglot_python_beer-song` | ✅ | 22.9s | 8 |
| `aider-polyglot/polyglot_python_affine-cipher` | ❌ | 17.1s | 8 |

结论与已知波动：

- 简单组可稳定全通过；中等组 `affine-cipher` / `beer-song` 会随采样波动（不同批次分别出现过通过和失败），需要更大样本量才能给出可靠准确率。
- 困难组失败的 5 个任务中，3 个是预算耗尽，1 个是模型流超时，1 个是容器内 TLS 证书问题导致的模型不可用；后者属于环境问题，重跑即可排除。
- `headless-terminal` 的 `test_background_commands` 也有偶发失败（服务未启动），复跑可通过。

## 设计说明与限制

- **本地优先**：服务只监听 loopback，模型密钥保存在 Keychain，数据库地址只从环境变量读取。
- **审批默认关闭**：`cmd/euler` 当前使用 `AutoApprove`；需要人工确认高风险工具时，把 `Gate` 换回 `approvals` 服务即可。
- **评测镜像来源**：部分任务镜像只有 amd64 清单，在 arm64 机器上通过 Docker 模拟运行；Docker Hub 匿名限流时可用 `mirror.gcr.io` 拉取后重新打标签。
- **困难任务耗时**：困难组中 `gpt2-codegolf`、`make-mips-interpreter`、`llm-inference-batching-scheduler` 会用满 10 分钟预算仍未完成，这是任务复杂度而非环境问题；个别 trial 会因容器内 TLS 证书或模型流超时而失败，需要通过 `agent/result.json` 的 `error_code` 区分环境失败与任务失败。
