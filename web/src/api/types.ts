// 与后端 REST DTO 一一对应的类型定义（internal/modelprofile、internal/session、
// internal/workspace、internal/api）。所有 ID 为 UUID 字符串，时间为 RFC3339。

export interface ModelProfile {
  id: string
  name: string
  base_url: string
  model: string
  secret_kind: 'keychain' | 'environment'
  secret_ref: string
}

export interface CreateModelProfileInput {
  name: string
  base_url: string
  model: string
  secret_kind: 'keychain' | 'environment'
  secret_ref: string
  api_key?: string
}

export interface UpdateModelProfileInput {
  name: string
  base_url: string
  model: string
  secret_ref?: string
  // 留空表示保留原有密钥。
  api_key?: string
}

// ListModelsInput 使用已填写的 Base URL 与凭据远程拉取模型列表；
// profile_id 用于编辑时复用后端已保存的密钥。
export interface ListModelsInput {
  base_url: string
  api_key?: string
  secret_kind?: 'keychain' | 'environment'
  secret_ref?: string
  profile_id?: string
}

export interface ModelListResult {
  models: string[]
}

export interface DeleteModelProfileResult {
  rebound_sessions: number
}

export interface WorkspaceView {
  id: string
  path: string
}

export interface ModelView {
  id: string
  name: string
  base_url: string
  model: string
}

export interface SessionView {
  id: string
  title: string
  status: string
  workspace: WorkspaceView
  model: ModelView
  created_at: string
  updated_at: string
  last_active_at: string
}

export interface CreateSessionInput {
  // 不传时后端使用默认模型（最早创建的模型配置）。
  profile_id?: string
  path: string
}

export interface CreateSessionResult extends SessionView {
  workspace_warning: boolean
}

// MessagePart 是消息 parts 中的一个有序片段：正文、思考或工具调用。
// 工具片段只保存 tool_calls 的标识，详情由消息携带的 tool_calls 补齐。
export type MessagePart =
  | { type: 'text'; text: string }
  | { type: 'reasoning'; text: string }
  | { type: 'tool'; tool_call_id: string }

export interface MessageView {
  id: string
  run_id?: string | null
  session_seq: number
  role: string
  content: string
  parts?: MessagePart[]
  tool_calls?: ToolCallView[]
  status: string
  created_at: string
  completed_at?: string | null
}

export interface MessagePage {
  messages: MessageView[]
  next_after: number
}

// FileNode 对应后端 workspace.FileNode：工作区文件树的一个节点。
export interface FileNode {
  path: string
  type: 'dir' | 'file'
  size: number
  mod_time: string
}

// WorkspaceDirectory / DirectoryListing 对应后端 workspace.Directory / Listing，
// 用于逐层浏览本机目录。
export interface WorkspaceDirectory {
  name: string
  path: string
}

export interface DirectoryListing {
  current_path: string
  parent_path: string | null
  directories: WorkspaceDirectory[]
}

export interface APIErrorBody {
  code: string
  message: string
  details?: unknown
}

// Run / 工具调用 / 审批 DTO（internal/persistence、internal/run）。
export type RunStatus =
  | 'queued'
  | 'running'
  | 'waiting_approval'
  | 'succeeded'
  | 'failed'
  | 'cancelled'
  | 'interrupted'

export interface RunView {
  id: string
  session_id: string
  status: RunStatus
  next_event_seq: number
  error_code?: string | null
  error_message?: string | null
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export type ToolCallStatus =
  | 'pending'
  | 'waiting_approval'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'rejected'
  | 'cancelled'

export interface ToolCallView {
  id: string
  run_id: string
  approval_id?: string | null
  name: string
  arguments_json: unknown
  status: ToolCallStatus
  result_json: unknown
  error_message?: string | null
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'cancelled'

export interface ApprovalView {
  id: string
  run_id: string
  tool_call_id: string
  status: ApprovalStatus
  requested_payload_json: unknown
  decision_payload_json?: unknown
  requested_at: string
  decided_at?: string | null
}

export interface RunSnapshot {
  run: RunView
  tool_calls: ToolCallView[]
  approvals: ApprovalView[]
  last_event_seq: number
}

export interface StartRunResult extends RunSnapshot {
  user_message: MessageView
  assistant_message: MessageView
  workspace_warning: boolean
}

// SSE Envelope：id 行即 seq，payload 为内嵌 JSON 对象。
export interface RunEvent {
  id: string
  run_id: string
  seq: number
  type: string
  payload: Record<string, unknown>
  created_at: string
}

export interface ApprovalDecisionInput {
  approvalId: string
  approved: boolean
  reason?: string
}

export interface ApprovalDecisionRecord {
  id: string
  run_id: string
  tool_call_id: string
  status: ApprovalStatus
  requested_at: string
  decided_at?: string | null
  decision: { approved: boolean; status: ApprovalStatus; reason?: string }
}

// 审批请求 payload：file_write/file_patch 带 diff，file_delete 只有 path，
// shell 带完整命令、目录与超时。
export interface FileApprovalPayload {
  path: string
  diff?: string
}

export interface ShellApprovalPayload {
  command: string
  dir: string
  timeout_seconds: number
}
