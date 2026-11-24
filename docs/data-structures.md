# Euler 数据结构说明

本文说明 Euler 在持久化、领域模型、HTTP 接口、事件流和工具协议中使用的数据结构。字段以当前代码为准，数据库定义见 [migrations](../migrations/000001_init.up.sql)、[migrations](../migrations/000002_message_parts.up.sql)，Go 模型见 [models.go](../internal/persistence/models.go)，前端类型见 [types.ts](../web/src/api/types.ts)。

## 1. 分层

数据在四层之间流转，字段名保持一致：

| 层 | 位置 | 说明 |
|---|---|---|
| 持久化模型 | `internal/persistence` | GORM 结构体，对应 Postgres 表 |
| 领域模型 / 服务 | `internal/session`、`internal/run`、`internal/event`、`internal/approval`、`internal/tools`、`internal/workspace` | 状态机、事务边界与业务校验 |
| HTTP DTO | 各包的 `handler.go` | JSON 请求体与响应体，`snake_case` 字段 |
| 前端类型 | `web/src/api/types.ts` | 与 HTTP DTO 一一对应的 TypeScript 接口 |

所有 ID 均为 UUID；所有时间均为 RFC3339 / `timestamptz`（UTC 存储，接口序列化为带时区字符串）。

## 2. 实体关系

```
model_profiles 1 ─── n sessions n ─── 1 workspaces
                         │
                         1
                         │
                         n
                       runs 1 ─── n messages
                         │
                         ├──── n tool_calls 1 ─── 0..1 approvals
                         └──── n run_events
```

- 会话绑定一个工作区和一个模型配置；切换模型或工作区只影响后续 run。
- 一个 run 对应一条用户消息和一条助手消息；助手消息通过 `parts` 记录真实对话顺序。
- 工具调用与审批都按 `run_id` 归属；审批与工具调用互相引用。
- 事件只属于 run，按 `(run_id, seq)` 单调递增。

## 3. 数据库表

外键全部为 `ON DELETE RESTRICT`。删除会话时由 [lifecycle.go](../internal/session/lifecycle.go) 按依赖逆序手工清理，工作区记录保留（多个会话可能共享同一目录）。

### 3.1 `workspaces`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 工作区 ID |
| `path` | text NOT NULL | 规范化后的绝对目录路径 |
| `created_at` | timestamptz | 创建时间 |

`path` 只有普通索引，不唯一；并发创建相同路径时由 `pg_advisory_xact_lock(hashtext(path))` 串行化。

### 3.2 `model_profiles`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 模型配置 ID |
| `name` | text NOT NULL | 展示名称 |
| `base_url` | text NOT NULL | OpenAI 兼容端点 |
| `model` | text NOT NULL | 模型名 |
| `secret_kind` | text NOT NULL | `keychain` 或 `environment` |
| `secret_ref` | text NOT NULL | 密钥环引用或环境变量名 |
| `created_at` / `updated_at` | timestamptz | 时间戳 |

密钥本身不落库：`keychain` 存系统密钥环（跨平台：macOS Keychain / Windows 凭据管理器 / Linux Secret Service），`environment` 从同名环境变量读取。API 响应中的 `Profile` 永远不包含密钥。

### 3.3 `sessions`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 会话 ID |
| `workspace_id` | uuid FK → `workspaces.id` | 工作目录 |
| `model_profile_id` | uuid FK → `model_profiles.id` | 模型配置 |
| `title` | text NOT NULL | 默认取工作目录 basename，可重命名 |
| `status` | text NOT NULL | `active` / `archived`（当前只写入 `active`，`archived` 为预留） |
| `created_at` / `updated_at` | timestamptz | 时间戳 |
| `last_active_at` | timestamptz | 列表排序依据 |

索引：`sessions_last_active_idx (last_active_at DESC)`。

### 3.4 `runs`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | run ID |
| `session_id` | uuid FK → `sessions.id` | 所属会话 |
| `status` | text NOT NULL | 见 4.1 |
| `next_event_seq` | bigint NOT NULL ≥ 1 | 下一条事件序号，初始为 1 |
| `error_code` | text | 失败错误码 |
| `error_message` | text | 脱敏后的失败原因 |
| `created_at` | timestamptz | 入队时间 |
| `started_at` | timestamptz | 开始执行时间 |
| `finished_at` | timestamptz | 终态时间 |

关键约束：

- `runs_one_active_idx`：同一 `session_id` 最多只有一条 `queued` / `running` / `waiting_approval` 的 run（部分唯一索引）。
- `next_event_seq` 在追加事件时于同一事务内 `+1`，因此 `last_event_seq = next_event_seq - 1`。

### 3.5 `messages`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 消息 ID |
| `session_id` | uuid FK → `sessions.id` | 所属会话 |
| `run_id` | uuid FK → `runs.id`，可空 | 触发该消息的 run |
| `session_seq` | bigint NOT NULL > 0 | 会话内单调递增序号 |
| `role` | text NOT NULL | `user` / `assistant` / `system` |
| `content` | text NOT NULL | 纯文本正文（流式期间持续更新） |
| `parts` | jsonb NOT NULL DEFAULT `[]` | 有序片段，见 5 |
| `status` | text NOT NULL | 见 4.2 |
| `created_at` | timestamptz | 创建时间 |
| `completed_at` | timestamptz | 完成时间 |

约束与索引：

- `sessions_message_seq_idx`：`(session_id, session_seq)` 唯一。
- `messages_run_idx (run_id)`。

### 3.6 `tool_calls`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 工具调用 ID，也是 `parts` 中工具片段的引用 |
| `run_id` | uuid FK → `runs.id` | 所属 run |
| `approval_id` | uuid，可空 | 关联审批 |
| `name` | text NOT NULL | 工具名 |
| `arguments_json` | jsonb NOT NULL | 模型传入参数（已脱敏） |
| `status` | text NOT NULL | 见 4.3 |
| `result_json` | jsonb | observation JSON（已脱敏） |
| `error_message` | text | 工具执行错误 |
| `created_at` / `started_at` / `finished_at` | timestamptz | 时间戳 |

索引：`tool_calls_run_idx (run_id, created_at)`。

### 3.7 `approvals`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 审批 ID |
| `run_id` | uuid FK → `runs.id` | 所属 run |
| `tool_call_id` | uuid FK → `tool_calls.id` | 被审批的工具调用 |
| `status` | text NOT NULL | 见 4.4 |
| `requested_payload_json` | jsonb NOT NULL | 审批请求载荷 |
| `decision_payload_json` | jsonb | 决策载荷 |
| `requested_at` / `decided_at` | timestamptz | 时间戳 |

`approvals_tool_call_pending_idx`：同一工具调用最多一条 `pending` 审批。`tool_calls.approval_id` 与 `approvals.tool_call_id` 互相引用，因此删除时必须先清空 `tool_calls.approval_id`。

### 3.8 `run_events`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid PK | 事件 ID |
| `run_id` | uuid FK → `runs.id` | 所属 run |
| `seq` | bigint NOT NULL > 0 | run 内序号 |
| `type` | text NOT NULL | 事件类型，见 7 |
| `payload_json` | jsonb NOT NULL | 事件载荷 |
| `created_at` | timestamptz | 写入时间 |

`run_events_seq_idx`：`(run_id, seq)` 唯一，是 SSE 回放与实时补洞的顺序依据。

## 4. 枚举与状态机

### 4.1 Run 状态

| 状态 | 含义 |
|---|---|
| `queued` | 已创建，等待 worker 执行 |
| `running` | 正在执行模型/工具循环 |
| `waiting_approval` | 停在审批关卡 |
| `succeeded` | 成功结束 |
| `failed` | 失败结束，带 `error_code` / `error_message` |
| `cancelled` | 用户主动取消 |
| `interrupted` | 服务退出或启动恢复时强制修复 |

合法迁移（[types.go](../internal/run/types.go)）：

| 起点 | 可迁移到 |
|---|---|
| `queued` | `running`、`cancelled`、`failed` |
| `running` | `waiting_approval`、`succeeded`、`failed`、`cancelled` |
| `waiting_approval` | `running`、`cancelled`、`failed` |

`interrupted` 只由启动恢复路径写入，不参与常规状态机。重复进入同一终态是幂等的，不会重复写事件。

失败错误码：

| `error_code` | 触发条件 |
|---|---|
| `run_timeout` | 超过单次 run 时长上限或上下文截止时间 |
| `model_timeout` | 单次模型请求空闲超时 / 总时长超时 |
| `model_unavailable` | 模型请求失败（网络、鉴权、服务端错误等） |
| `tool_call_limit` | 工具调用次数超过预算 |
| `run_failed` | 其他未分类失败 |

### 4.2 Message 状态

`streaming` → `completed` / `failed` / `cancelled` / `interrupted`。用户消息在创建时即为 `completed`。

### 4.3 ToolCall 状态

| 状态 | 进入时机 |
|---|---|
| `pending` | 工具调用记录创建，参数已落库 |
| `waiting_approval` | 审批已打开，run 停在 `waiting_approval` |
| `running` | 审批通过，工具即将执行 |
| `succeeded` | 执行成功 |
| `failed` | 执行失败或预算耗尽 |
| `rejected` | 用户拒绝审批 |
| `cancelled` | run 被取消或中断 |

只读工具（`file_read`、`file_list`、`file_search`）不经过审批，直接从 `pending` → `succeeded` / `failed`。

### 4.4 Approval 状态

`pending` → `approved` / `rejected` / `cancelled`。同一审批只接受第一次决策；后续请求返回 409。

### 4.5 模型密钥类型

| `secret_kind` | 密钥来源 | `secret_ref` 含义 |
|---|---|---|
| `keychain` | 系统密钥环 | 密钥环服务名，创建时可省略，默认 `profile/<id>` |
| `environment` | 环境变量 | 环境变量名，必须匹配 `^[A-Za-z_][A-Za-z0-9_]*$` |

## 5. 消息片段 `messages.parts`

`parts` 是按发生顺序排列的 JSON 数组，用来还原「文本 → 工具调用 → 文本」的真实时序：

```json
[
  { "type": "reasoning", "text": "先确认目录结构" },
  { "type": "text", "text": "我来看看当前目录。" },
  { "type": "tool", "tool_call_id": "2f0c..." },
  { "type": "text", "text": "任务已完成。" }
]
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `type` | string | `text` / `reasoning` / `tool` |
| `text` | string | `text` 与 `reasoning` 的正文，其他类型省略 |
| `tool_call_id` | string | `tool` 片段引用的 `tool_calls.id` |

约定：

- 工具片段的名称、参数与结果仍以 `tool_calls` 表为权威来源，`parts` 只保存引用。
- 连续且同类型的文本/思考片段在写入时会合并；读取时再执行一次 `CoalesceParts` 兼容旧数据。
- `parts` 缺失或损坏时，读取方回退为 `content` 的单个文本片段。
- 写入数据库前，正文与片段文本都经过密钥脱敏。

## 6. HTTP DTO

接口统一位于 `/api/v1`（`/health` 与 `/api/v1/bootstrap` 除外），错误响应统一为：

```json
{ "code": "invalid_request", "message": "可读的中文提示", "details": null }
```

### 6.1 模型配置

`Profile`（响应，不含密钥）：

| 字段 | 类型 |
|---|---|
| `id` | uuid |
| `name` | string |
| `base_url` | string |
| `model` | string |
| `secret_kind` | `keychain` \| `environment` |
| `secret_ref` | string |

请求体：

| 接口 | 请求体 |
|---|---|
| `POST /model-profiles` | `{ name, base_url, model, secret_kind, secret_ref, api_key }` |
| `PUT /model-profiles/:id` | `{ name, base_url, model, secret_ref, api_key }`，`api_key` 留空表示保留原密钥 |
| `POST /model-profiles/models` | `{ base_url, api_key?, secret_kind?, secret_ref?, profile_id? }` |

其它响应：

- `GET /model-profiles` → `Profile[]`
- `POST /model-profiles/models` → `{ "models": ["..."] }`
- `DELETE /model-profiles/:id` → `{ "rebound_sessions": 3 }`，被删除配置上的会话会自动改绑到默认配置

### 6.2 会话与消息

`SessionView`：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid | 会话 ID |
| `title` | string | 标题 |
| `status` | string | 会话状态 |
| `workspace` | `{ id, path }` | 工作区摘要 |
| `model` | `{ id, name, base_url, model }` | 模型摘要，不含密钥 |
| `created_at` / `updated_at` / `last_active_at` | string | 时间戳 |

`CreateSessionResult` = `SessionView` + `workspace_warning: boolean`（同一目录下其他会话存在活跃 run 时为 `true`）。

`MessageView`：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uuid | 消息 ID |
| `run_id` | uuid? | 所属 run |
| `session_seq` | number | 会话内序号 |
| `role` | string | 角色 |
| `content` | string | 纯文本正文 |
| `parts` | `MessagePart[]` | 有序片段 |
| `tool_calls` | `ToolCallView[]?` | 仅助手消息携带，按 run 补齐 |
| `status` | string | 消息状态 |
| `created_at` / `completed_at` | string? | 时间戳 |

`MessagePage`：`{ messages: MessageView[], next_after: number }`。`next_after` 为当前页最后一条的 `session_seq`，为 0 或未变化表示没有更多。

请求体：

| 接口 | 请求体 | 说明 |
|---|---|---|
| `POST /sessions` | `{ profile_id?, path? }` | 两者都可省略；省略 `profile_id` 用最早创建的模型，省略 `path` 在 `workspace_root` 下按会话 ID 建目录 |
| `PATCH /sessions/:id` | `{ model_profile_id }` 或 `{ workspace_path }` | 二者必须且只能提供一个；切换工作区要求会话空闲，否则 409 |
| `POST /sessions/:id/runs` | `{ content }` | `content` 去除空白后不能为空；响应 202 |

列表查询参数：`GET /sessions?q=&workspace_path=&active_run=true|false`。

### 6.3 Run、工具调用与审批

`RunView`：

| 字段 | 类型 |
|---|---|
| `id` | uuid |
| `session_id` | uuid |
| `status` | `RunStatus` |
| `next_event_seq` | number |
| `error_code` / `error_message` | string? |
| `created_at` / `started_at` / `finished_at` | string? |

`ToolCallView`：`{ id, run_id, approval_id?, name, arguments_json, status, result_json, error_message?, created_at, started_at?, finished_at? }`。

`ApprovalView`：`{ id, run_id, tool_call_id, status, requested_payload_json, decision_payload_json?, requested_at, decided_at? }`。

`RunSnapshot`：`{ run, tool_calls, approvals, last_event_seq }`，由 `GET /runs/:id` 与 `POST /runs/:id/cancel` 返回。

`StartRunResult` = `RunSnapshot` + `{ user_message, assistant_message, workspace_warning }`，由 `POST /sessions/:id/runs` 返回。

审批决策请求：`POST /approvals/:id/decision`，请求体 `{ approved: boolean, reason?: string }`；`approved` 必须显式提供。响应为审批记录 `Record`：

```json
{
  "id": "uuid",
  "run_id": "uuid",
  "tool_call_id": "uuid",
  "status": "approved",
  "requested_at": "2026-11-22T10:00:00Z",
  "decided_at": "2026-11-22T10:00:03Z",
  "decision": { "approved": true, "status": "approved", "reason": "允许写入" }
}
```

### 6.4 工作区与目录浏览

`FileNode`（工作区文件树）：`{ path, type: "dir"|"file", size, mod_time }`，单次最多 1000 个节点。

`Directory`：`{ name, path }`。

`Listing`（目录浏览）：`{ current_path, parent_path: string|null, directories: Directory[] }`，最多 200 个子目录。

| 接口 | 说明 |
|---|---|
| `GET /fs/directories?path=` | 省略 `path` 时从用户主目录开始 |
| `POST /fs/directories` | 请求体 `{ parent_path, name }`，`name` 必须是单层目录名 |
| `GET /workspaces/:id/files` | 返回 `FileNode[]` |
| `GET /workspaces/:id/files/content?path=&download=` | 预览受 `preview_bytes` 限制；`download=true` 流式下载完整文件 |

## 7. 事件流

事件先写入 `run_events`，再广播给在线订阅者。`GET /api/v1/runs/:id/events` 同时承担历史回放与实时推送：先读取 `seq > after` 的已持久化事件，再消费实时事件，并按序号补洞、丢弃重复事件。

SSE 帧格式：

```
id: 7
data: {"id":"...","run_id":"...","seq":7,"type":"message.delta","payload":{"message_id":"...","delta":"..."},"created_at":"..."}

```

- SSE 的 `id` 行等于事件 `seq`。
- 客户端可用查询参数 `after=<seq>` 或请求头 `Last-Event-ID` 指定断点，服务端取两者较大值。
- 每 15 秒发送一次 `: heartbeat` 注释帧。
- run 结束后连接不会自动关闭；客户端收到终态事件（`run.completed` / `run.failed` / `run.cancelled` / `run.interrupted`）后应主动断开。

事件载荷：

| 事件类型 | 载荷字段 |
|---|---|
| `run.started` | `session_id`、`assistant_message_id` |
| `message.started` | `message_id`、`role` |
| `message.delta` | `message_id`、`delta` |
| `message.reasoning` | `message_id`、`delta` |
| `tool.started` | `tool_call_id`、`name`、`arguments`（对象）、`model_call_id` |
| `tool.completed` | `tool_call_id`、`name`、`result`（observation 对象） |
| `tool.failed` | `tool_call_id`、`name`、`result`（`status` 为 `failed` 的对象） |
| `approval.requested` | `approval_id`、`tool_call_id`、`name`、`request`（待审批参数） |
| `approval.resolved` | `approval_id`、`tool_call_id`、`status`、`approved`、`reason` |
| `run.completed` | `{}` |
| `run.failed` | `error_code?`、`error_message?`（非空时才出现） |
| `run.cancelled` | `{}` |
| `run.interrupted` | `{}` |

## 8. 工具输入与输出

Agent 共有 8 个工具。所有 observation 都是 JSON 对象，顶层固定包含 `status`：成功为 `completed`，失败为 `failed`。

| 工具 | 输入 | 成功输出（另含 `status: "completed"`） |
|---|---|---|
| `file_read` | `path`、`offset?`（默认 1）、`limit?`（默认 2000，上限 5000） | `path`、`content`、`start_line`、`end_line`、`has_more`、`next_offset?` |
| `file_list` | `path?`、`cursor?`、`limit?`（默认 200，上限 500） | `entries: [{ path, type, size }]`、`has_more`、`next_cursor` |
| `file_search` | `query`（只匹配路径）、`cursor?`、`limit?` | `matches: string[]`、`has_more`、`next_cursor` |
| `file_write` | `path`、`content` | `path` |
| `file_patch` | `path`、`old_string`、`new_string` | `path` |
| `file_delete` | `path` | `path` |
| `shell` | `command`、`timeout_seconds?` | `output`、`exit_code`、`timed_out`、`truncated` |
| `submit` | `summary` | `summary` |

分页契约：

- `file_read` 用 `offset` / `limit` 按行分页，`next_offset` 是下一页起始行号；输出超过 `max_output_bytes` 时按行回退，并保证 `start_line`/`end_line` 与实际返回文本一致。
- `file_list` 与 `file_search` 用游标分页，`next_cursor` 是上一页最后一条路径/结果的字典序位置；列表被输出预算裁剪时也会回退 `next_cursor`。
- `file_search` 只搜索路径，不读文件内容；内容检索应使用 `shell`（如 `grep`）。

写类工具先返回待审批参数，审批通过后才落盘：

| 工具 | 审批载荷 `request` |
|---|---|
| `file_write` / `file_patch` | `{ path, diff }` |
| `file_delete` | `{ path, diff: "deleted: <path>\n" }` |
| `shell` | `{ command, dir, timeout_seconds }` |

错误 observation：

```json
{ "status": "failed", "error": { "code": "file_not_found", "message": "文件不存在：a.txt" } }
```

文件错误码：

| `code` | 含义 |
|---|---|
| `file_not_found` | 文件或目录不存在 |
| `path_outside_workspace` | 路径逃逸出工作区 |
| `file_too_large` | 文件超过读取上限 |
| `line_too_long` | 存在超长行，无法按行读取 |
| `not_regular_file` | 目标不是普通文件 |
| `not_directory` | 目标不是目录 |
| `patch_ambiguous` | `old_string` 不唯一或不存在 |
| `not_deletable` | 只能删除工作区内的普通文件 |
| `file_error` | 未分类文件错误 |

Shell 错误码：`shell_timeout`（超时）、`shell_error`（启动失败）、`shell_exit`（退出码非零）。

## 9. 不变式

- 一个会话同时最多一个活跃 run，由部分唯一索引兜底；创建会话/启动 run 时还会检查同一工作区的其他活跃 run 并返回告警。
- 消息序号 `(session_id, session_seq)` 唯一，用户消息与助手消息在同一事务中连续分配。
- 事件序号 `(run_id, seq)` 唯一且连续；`seq` 由锁定 `runs.next_event_seq` 后自增分配，保证事务内顺序。
- `parts` 决定展示顺序，`tool_calls` 决定工具详情；渲染方不得把工具列表统一挪到消息末尾。
- `tool_calls.arguments_json`、`result_json` 与消息正文在落库前都会脱敏，密钥不会进入数据库、事件或日志。
- 所有工作区路径在使用前都会解析 symlink 并校验包含关系；写操作在 `Apply` 时再次校验，防止校验后被替换为逃逸链接。
- 游标分页只作用于 Agent 工具层；前端文件树与消息历史使用各自的分页参数，不受工具层的 `max_output_bytes` 影响。
