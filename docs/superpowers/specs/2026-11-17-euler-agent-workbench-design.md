# Euler 本机通用 Agent：基础工作台设计

- 日期：2026-11-17
- 状态：待用户审阅
- 阶段：第一阶段，基础工作台端到端竖切

## 1. 背景与目标

Euler 的目标是构建一个本机单用户、多会话的通用 Agent 平台。每个会话绑定一个本机工作目录；用户未主动选择时，系统自动创建独立目录。Agent 的工作产出保存在该目录中。

长期目标包括工具执行、插件、子 Agent、长期记忆、知识库、权限、观测与评估。项目按阶段交付，本文只详细规定第一阶段的基础工作台，同时为后续能力保留稳定边界。

第一阶段的核心目标是跑通以下端到端流程：

1. 创建会话并选择或自动创建工作区。
2. 通过 OpenAI 兼容接口进行多轮流式对话。
3. Agent 使用文件与 Shell 工具完成任务。
4. 用户在对话流中审批或拒绝敏感操作。
5. 用户停止任务、刷新页面或断线重连后，仍能查看一致的消息与执行状态。
6. 用户在工作台中预览和下载工作区产物。

## 2. 已确认约束

- 本机单用户，不要求账号、租户或多人权限体系。
- 多会话可并行；每个会话同一时刻只有一个活跃 Run。
- 后端使用 Go，Agent 内核使用 CloudWeGo Eino。
- HTTP 使用 Gin，ORM 使用 GORM，结构化存储使用 PostgreSQL。
- 前端使用 React、TypeScript、Vite、Ant Design、TanStack Query 和 Zustand。
- 模型通过 OpenAI 兼容接口接入，不绑定具体供应商。
- Agent 的 Shell 在宿主机执行，不以 Docker 作为 Agent 沙箱。
- 工作区默认独立，已有目录允许被多个会话复用。
- 跨平台设计，第一阶段只在 macOS 完成端到端验证。
- 交互参考 Claude，但不复制其品牌资源，也不绑定 Claude 模型。

## 3. 第一阶段范围

### 3.1 包含

- 本机模型配置和密钥引用管理。
- 会话创建、列表、搜索、历史消息与工作区绑定。
- 本机目录浏览、手动路径输入和默认工作区创建。
- OpenAI 兼容模型的流式对话与工具调用。
- 工作区文件读取、列目录、写入、补丁和删除工具。
- 宿主机 Shell 工具、命令审批、超时、输出限制和停止执行。
- PostgreSQL 持久化的 Run、工具调用、审批和事件日志。
- SSE 事件推送、断线补拉和页面刷新恢复。
- Claude 风格三栏工作台、工具时间线、审批卡片和产物预览。
- 自动化测试及本机端到端验收。

### 3.2 不包含

- 多用户、远程服务器工作区和租户隔离。
- Docker 或虚拟机中的 Agent 执行隔离。
- 持久 Agent 检查点、进程崩溃后的自动续跑和副作用自动重放。
- 子 Agent、长期记忆、知识库、RAG 和向量检索。
- 自定义插件系统、MCP、OpenTelemetry 和自动化评估平台。
- 完整 IDE 编辑器、Git 工作台和移动端适配。
- 自动更新、安装包签名和桌面应用封装。

## 4. 总体架构

采用单进程、模块化单体。Go 服务同时提供 REST API、SSE、Agent 执行和前端静态资源；PostgreSQL 是唯一结构化数据存储。

```text
React Web
  ├─ REST：会话、工作区、模型配置、审批、文件
  └─ SSE：消息增量、Run 状态、工具状态、审批事件
          │
          ▼
Go 单进程
  ├─ API 与本地访问保护
  ├─ Session / Workspace / Model Profile
  ├─ Run Manager 与 Event Store
  ├─ Eino Agent Runtime
  ├─ File Tools / Shell Tools / Approval Service
  └─ React 静态资源托管
          │
          ├──────────────► PostgreSQL
          ├──────────────► OpenAI 兼容模型服务
          ├──────────────► 本机工作区与 Shell
          └──────────────► OS Keychain / 环境变量
```

开发环境由 Vite 提供前端开发服务器并代理 API；生产模式由 Go 托管前端构建产物，保持同源。第一阶段不引入 Redis、消息队列、微服务或 Kubernetes。Docker Compose 仅用于启动开发用 PostgreSQL，不承载 Agent 工具执行。

## 5. 技术底座与模块边界

| 层 | 选型 | 责任 |
| --- | --- | --- |
| Web | React、TypeScript、Vite、Ant Design | 工作台、对话、审批和文件预览 |
| 服务端状态 | TanStack Query | API 数据获取、缓存和失效 |
| 本地运行状态 | Zustand | 流式消息、连接状态、面板和草稿 |
| HTTP | Go、Gin | REST、SSE 和静态资源 |
| Agent | Eino | ChatModel、ReAct、工具调用与流式编排 |
| 数据 | PostgreSQL、GORM | 业务数据、事件和状态持久化 |
| 数据库迁移 | golang-migrate | 可重复、可审查的 schema 变更 |
| 工作区安全路径 | `cyphar/filepath-securejoin` | 工作区内安全解析与打开路径 |
| 系统钥匙串 | `zalando/go-keyring` | macOS Keychain 及跨平台密钥适配 |
| 日志 | Go `log/slog` | 结构化日志与敏感字段过滤 |
| 测试 | Go test、Vitest、Testing Library、Playwright | 单元、集成和端到端验证 |

内部模块职责：

- **Session**：会话生命周期、消息顺序和会话查询。
- **Workspace**：路径规范化、默认目录创建、目录浏览和产物访问。
- **Model Profile**：OpenAI 兼容配置、密钥引用和模型能力校验。
- **Run Manager**：并发约束、状态机、取消、超时和启动后状态修复。
- **Agent Runtime**：将持久消息转换为 Eino 输入，执行模型与工具循环。
- **Tool Registry**：第一阶段内置文件和 Shell 工具，后续可接入外部工具。
- **Approval**：审批创建、决策、持久化和超时/取消联动。
- **Event Store**：Run 事件顺序、落库、实时发布和断线补拉。
- **File Preview**：工作区文件树、有限大小预览和下载，不复制产物到数据库。

模块只通过本模块公开服务接口交互，API 层不直接操作 GORM 实体，Agent 不直接读取 HTTP 请求或写入 SSE。

## 6. 工作区规则

### 6.1 创建与绑定

- 应用配置保存在 `~/.euler/config.yaml`，包含默认工作区根目录、监听地址和 Agent 限制；数据库连接通过 `EULER_DATABASE_URL` 环境变量提供。
- 默认工作区根目录为 `~/EulerWorkspaces`，可由应用配置修改。
- 用户未选择目录时，系统创建 `~/EulerWorkspaces/<session-id>`，权限默认为仅当前用户可访问。
- 用户可输入绝对路径，或通过后端提供的目录浏览器选择已有目录。
- 创建会话前，后端验证目标存在、为目录且当前进程可访问；自动创建场景同时验证父目录。
- 路径保存前转为绝对路径、清理相对段并解析符号链接。
- 工作区在会话创建后不可更换。需要其他目录时创建新会话。
- 工作区只保存用户项目、输入文件和 Agent 产出。配置、数据库和密钥不放入工作区。

文件系统操作和数据库写入无法组成一个事务。自动创建流程先创建目录，再写数据库；数据库失败时仅清理由本次操作新建且仍为空的目录，绝不清理用户已有目录。

### 6.2 复用与并发

- 一个会话绑定一个工作区；不同会话可绑定相同路径。
- 当同路径的其他会话存在活跃 Run 时，创建和发送消息界面显示并发修改警告。
- 第一阶段不做工作区全局锁，也不承诺解决并发写入冲突。
- 每个会话只能有一个 `queued`、`running` 或 `waiting_approval` 状态的 Run。
- 不同会话的 Run 可由 Run Manager 并行执行。

## 7. 数据模型

所有 ID 使用 UUID，时间使用 UTC `timestamptz`。JSONB 只用于结构会变化的事件和工具负载，核心查询字段使用明确列。

### 7.1 Workspaces

- `id`
- `path`：规范化后的绝对路径，不要求跨会话唯一。
- `created_at`

### 7.2 Sessions

- `id`
- `workspace_id`
- `model_profile_id`
- `title`
- `status`：`active` 或 `archived`
- `created_at`、`updated_at`、`last_active_at`

会话内消息序号通过锁定会话记录或使用会话序号计数器分配，避免并发产生重复序号。

### 7.3 Messages

- `id`、`session_id`、`run_id`（用户触发消息可关联其创建的 Run）
- `session_seq`：会话内严格递增序号。
- `role`：`user`、`assistant` 或 `system`。
- `content`：面向用户展示的文本内容。
- `status`：`streaming`、`completed`、`failed`、`cancelled` 或 `interrupted`。
- `created_at`、`completed_at`

工具参数和结果不塞入助手消息文本，由 `tool_calls` 单独保存。

### 7.4 Runs

- `id`、`session_id`
- `status`：`queued`、`running`、`waiting_approval`、`succeeded`、`failed`、`cancelled` 或 `interrupted`。
- `next_event_seq`
- `error_code`、`error_message`
- `created_at`、`started_at`、`finished_at`

数据库约束与应用层 Run Manager 共同保证一个会话只有一个活跃 Run。

### 7.5 Tool Calls

- `id`、`run_id`、`approval_id`（可空）
- `name`、`arguments_json`
- `status`：`pending`、`waiting_approval`、`running`、`succeeded`、`failed`、`rejected` 或 `cancelled`。
- `result_json`、`error_message`
- `created_at`、`started_at`、`finished_at`

### 7.6 Approvals

- `id`、`run_id`、`tool_call_id`
- `status`：`pending`、`approved`、`rejected` 或 `cancelled`。
- `requested_payload_json`、`decision_payload_json`
- `requested_at`、`decided_at`

### 7.7 Run Events

- `id`、`run_id`
- `seq`：Run 内严格递增序号，`run_id + seq` 唯一。
- `type`、`payload_json`、`created_at`

### 7.8 Model Profiles

- `id`、`name`、`base_url`、`model`
- `secret_kind`：`keychain` 或 `environment`。
- `secret_ref`：钥匙串条目或环境变量名，不是密钥本身。
- `created_at`、`updated_at`

## 8. Run 生命周期与事件

### 8.1 状态流转

```text
queued → running → succeeded
           │  ├────► failed
           │  ├────► cancelled
           │  └────► interrupted（仅进程恢复时修复）
           ▼
    waiting_approval → running
           │
           └────────► cancelled
```

- `failed` 表示本次 Run 无法继续，不删除已产生的消息、事件或文件。
- 用户拒绝一个工具不等于 Run 失败。拒绝结果作为工具观察反馈给 Agent，Agent 可调整方案或结束任务。
- 停止操作同时取消 Run context、待审批项和正在执行的子进程。
- 服务启动时将上次遗留的活跃 Run 修复为 `interrupted`，不自动重新执行。

### 8.2 事件类型

- `run.started`
- `message.started`、`message.delta`、`message.completed`
- `tool.started`、`tool.progress`、`tool.completed`、`tool.failed`
- `approval.requested`、`approval.resolved`
- `run.completed`、`run.failed`、`run.cancelled`、`run.interrupted`

事件负载带 `event_id`、`run_id`、`seq`、`type` 和时间。消息增量按小块批量落库，不要求每个 token 写一行数据库记录。

### 8.3 持久化与推送顺序

事件先写入 PostgreSQL，再发布给本进程中的 SSE 订阅者。SSE 连接建立时先注册临时订阅，再查询并回放 `after` 之后的事件，最后按序号去重并发送实时事件，避免回放期间的遗漏。

慢客户端可以断开并依据最后序号重连；事件权威来源始终是 PostgreSQL，不依赖内存队列保证持久性。

## 9. 工具与审批策略

### 9.1 文件工具

- 列目录、读取和搜索：仅限工作区内，可自动执行并记录工具事件。
- 写入、补丁和删除：仅限工作区内，执行前需要审批；写入和补丁展示 diff，删除展示目标路径。
- 所有操作重新校验规范化路径和符号链接，拒绝 `..` 逃逸、绝对路径注入和指向工作区外的符号链接。
- 文件预览同样执行路径校验，不允许借预览接口读取工作区外文件。

### 9.2 Shell 工具

- 每次调用都必须审批，不提供第一阶段的全局“始终允许”。
- 审批卡片显示完整命令、参数、工作目录和超时时间。
- 命令以会话工作区作为初始目录，但这不是安全边界；界面明确提示命令可访问当前 OS 用户有权限访问的其他资源。
- 构造 Shell 子进程环境时剔除所有模型配置引用的密钥环境变量，避免子进程通过继承父进程环境获得 API Key。
- 支持输出大小限制、执行超时、取消和进程组终止。
- macOS 第一阶段使用用户 Shell 的兼容执行方式；Windows 与 Linux 的命令和进程管理保留独立适配边界。

### 9.3 Agent 限制

Agent Runtime 对单次 Run 设置最大模型轮数、工具调用次数、执行时长和工具输出大小。达到限制时以明确错误结束，不允许无限 ReAct 循环。具体默认值写入应用配置，并可在配置文件中调整，不在第一阶段向模型开放修改。

## 10. API 与流式协议

API 统一使用 `/api/v1` 前缀。第一阶段主要端点：

| 方法与路径 | 用途 |
| --- | --- |
| `GET /health` | 服务与数据库健康状态 |
| `GET /fs/directories` | 浏览本机目录，不读取文件内容 |
| `GET/POST /model-profiles` | 查询或创建模型配置 |
| `GET/POST /sessions` | 会话列表或创建会话 |
| `GET /sessions/{id}` | 会话详情、工作区和模型信息 |
| `GET /sessions/{id}/messages` | 按序号分页查询历史消息 |
| `POST /sessions/{id}/runs` | 写入用户消息并创建 Run |
| `GET /runs/{id}` | 查询 Run、工具调用和审批快照 |
| `GET /runs/{id}/events?after={seq}` | SSE 回放并订阅事件 |
| `POST /runs/{id}/cancel` | 停止 Run |
| `POST /approvals/{id}/decision` | 批准或拒绝待审批工具 |
| `GET /workspaces/{id}/files` | 查询工作区文件树 |
| `GET /workspaces/{id}/files/content` | 安全预览或下载工作区文件 |

写入接口返回持久化后的资源，不由前端猜测状态。冲突请求返回 `409`，参数或路径错误返回 `400`，资源不存在返回 `404`，未批准的工具执行由工具状态表示，不伪装成 HTTP 授权错误。

SSE 使用事件序号作为 `id`。浏览器断线后使用最后确认的序号重新连接；前端按 `run_id + seq` 去重。API 错误使用统一结构：`code`、面向用户的 `message` 和可选 `details`，内部堆栈仅进入脱敏日志。

## 11. 前端工作台

### 11.1 布局

- 左侧：新建会话、历史搜索、最近活跃排序、运行状态和工作区名称。
- 中间：顶部显示工作区绝对路径、模型和 Run 状态；下方为消息与工具时间线；底部为消息输入和停止按钮。
- 右侧：可折叠文件树，支持文本、Markdown、图片预览和文件下载。
- 设置页：模型配置、Base URL、模型名和密钥引用管理。API Key 只写不回显。

### 11.2 新建会话

用户可选择模型、手动输入目录或浏览本机目录。目录留空时明确提示将自动创建默认工作区。若目录已被活跃会话使用，显示并发风险提示。

### 11.3 对话与审批

- 消息支持 Markdown、代码高亮、复制和流式渲染。
- 工具卡片展示参数摘要、工作目录、状态和结果摘要，不展示模型隐式思维链。
- 待审批操作在对话流中阻塞，展示批准和拒绝操作。
- 文件修改展示 diff，Shell 展示完整命令。
- Run 活跃时保留下一条消息草稿，但不并发启动第二个 Run。
- 用户可停止 Run；刷新或重连后恢复消息、工具进度和待审批状态。

TanStack Query 保存服务端快照，Zustand 只保存流式增量、连接状态和 UI 状态。事件完成后使相关查询失效并重新获取权威快照，避免本地状态与 PostgreSQL 长期分叉。

## 12. 本机安全与密钥

- 默认只监听 `127.0.0.1`；修改监听地址需要显式配置。
- Go 服务启动时生成本机会话令牌，通过 `HttpOnly`、`SameSite=Strict` Cookie 供同源页面调用 API。
- 修改数据的 API 校验 Origin；非预期来源直接拒绝。仅依赖 localhost 或 CORS 不足以防止其他网站发起本机请求。
- API Key 可写入操作系统钥匙串，或引用已有环境变量；PostgreSQL、工具参数、事件和日志均不保存明文密钥。
- macOS 使用系统 Keychain；跨平台密钥访问通过独立适配接口实现。
- 日志默认不记录授权头、Cookie、完整模型请求、完整文件内容和完整工具输出。
- 文件与 Shell 结果在发送给模型前受大小限制；界面提示使用云端模型时，相关对话和工具内容会发送给模型供应商。

## 13. 错误处理与恢复

- 模型网络、限流、鉴权和协议错误映射为稳定的内部错误码。
- 仅对尚无副作用的模型请求做有限指数退避重试；工具调用不自动盲目重放。
- 可恢复的工具错误作为结构化观察返回 Agent，让其修改参数或更换方案。
- 上下文取消贯穿 Run、模型流、文件工具和 Shell 子进程。
- PostgreSQL 不可用时健康检查失败，不启动新的 Agent Run。
- 服务重启后，遗留活跃 Run 和对应流式消息标记为 `interrupted`，待审批项取消。
- 用户可基于完整历史显式发起新的 Run 继续任务；系统不声称能恢复已消失的模型流或无损续跑子进程。
- 文件已经产生的修改保留，不由系统自动回滚。

## 14. 测试策略

### 14.1 Go 单元测试

- 工作区路径规范化、符号链接逃逸和文件预览边界。
- Run 与 Approval 状态机的合法和非法流转。
- 会话与 Run 的并发约束、消息和事件序号。
- 事件回放与实时订阅之间不遗漏、不重复。
- Shell 超时、输出截断和进程组取消。
- 密钥与敏感字段日志脱敏。

### 14.2 集成测试

- 使用真实 PostgreSQL 验证迁移、约束、事务和断线补拉。
- 使用模拟 OpenAI 兼容服务和 Eino 测试模型，覆盖文本流、工具调用、异常和取消。
- 使用临时目录验证文件工具及审批前后文件状态。
- 使用安全的测试命令验证 Shell 审批、拒绝、成功、失败和停止。

### 14.3 前端与端到端测试

- Vitest 和 Testing Library 覆盖消息渲染、审批卡片、连接状态和错误提示。
- Playwright 使用真实 Go 后端、PostgreSQL 和模拟模型完成端到端验证。
- 自动化测试不依赖真实付费模型；另提供手动模型供应商冒烟测试。

## 15. 第一阶段验收标准

1. 不选择目录创建会话时，系统在默认根目录创建独立工作区，Agent 产出位于其中。
2. 手动选择已有目录时，会话绑定规范化后的路径，并允许另一会话复用该目录。
3. 同一会话不能并行启动两个 Run，不同会话可并行执行。
4. 模型文本和工具状态可流式展示，刷新或 SSE 重连后无重复、无遗漏。
5. 文件写入、补丁、删除和每次 Shell 调用均可批准或拒绝；未批准前不产生对应副作用。
6. 文件工具不能通过相对路径或符号链接访问工作区外内容。
7. Shell 可停止并终止其子进程，超时和输出限制生效。
8. 工作区文件树可预览文本、Markdown、图片并下载产物。
9. 服务重启后遗留 Run 显示为 `interrupted`，历史仍可查看，用户可显式继续。
10. 数据库、事件、API 响应和日志中不出现明文 API Key。
11. macOS 上从创建会话到产物预览的 Playwright 端到端测试通过。

## 16. 后续阶段演进

### 第二阶段：Agent 内核

- 持久检查点和用户显式恢复。
- 上下文压缩、计划、任务预算和更细粒度执行策略。
- 基于 Eino ADK 的子 Agent 与 Agent-as-Tool。

### 第三阶段：扩展平台

- 优先接入 MCP，避免先发明私有插件协议。
- 长期记忆和基于 PostgreSQL/pgvector 的知识库。
- OpenTelemetry 观测、Trace 导出和自动化评估。

后续阶段复用 Session、Workspace、Run、Tool、Approval 和 Event Store 边界；新增能力不得绕过工作区校验、审批和事件持久化。
