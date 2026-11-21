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
  profile_id: string
  path: string
}

export interface CreateSessionResult extends SessionView {
  workspace_warning: boolean
}

export interface MessageView {
  id: string
  run_id?: string | null
  session_seq: number
  role: string
  content: string
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
