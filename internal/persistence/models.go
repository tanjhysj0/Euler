package persistence

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Workspace 是会话运行所针对的规范化目录。
type Workspace struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Path      string    `gorm:"column:path;not null"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// TableName 将 Workspace 映射到其数据库表。
func (Workspace) TableName() string { return "workspaces" }

// ModelProfile 指向一个兼容 OpenAI 的端点和一个密钥引用。
type ModelProfile struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name       string    `gorm:"not null"`
	BaseURL    string    `gorm:"column:base_url;not null"`
	Model      string    `gorm:"column:model;not null"`
	SecretKind string    `gorm:"not null"`
	SecretRef  string    `gorm:"not null"`
	CreatedAt  time.Time `gorm:"not null;default:now()"`
	UpdatedAt  time.Time `gorm:"not null;default:now()"`
}

// TableName 将 ModelProfile 映射到其数据库表。
func (ModelProfile) TableName() string { return "model_profiles" }

// Session 将工作区和模型配置绑定到一次对话。
type Session struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	WorkspaceID    uuid.UUID `gorm:"type:uuid;not null"`
	ModelProfileID uuid.UUID `gorm:"type:uuid;not null"`
	Title          string    `gorm:"not null"`
	Status         string    `gorm:"not null"`
	CreatedAt      time.Time `gorm:"not null;default:now()"`
	UpdatedAt      time.Time `gorm:"not null;default:now()"`
	LastActiveAt   time.Time `gorm:"not null;default:now()"`
}

// TableName 将 Session 映射到其数据库表。
func (Session) TableName() string { return "sessions" }

// Run 是会话内的一次 agent 执行。
type Run struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	SessionID    uuid.UUID  `gorm:"type:uuid;not null" json:"session_id"`
	Status       string     `gorm:"not null" json:"status"`
	NextEventSeq int64      `gorm:"not null;default:1" json:"next_event_seq"`
	ErrorCode    *string    `gorm:"column:error_code" json:"error_code"`
	ErrorMessage *string    `gorm:"column:error_message" json:"error_message"`
	CreatedAt    time.Time  `gorm:"not null;default:now()" json:"created_at"`
	StartedAt    *time.Time `gorm:"column:started_at" json:"started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at" json:"finished_at"`
}

// TableName 将 Run 映射到其数据库表。
func (Run) TableName() string { return "runs" }

// Message 是用户可见的对话消息，带有会话级序号。
type Message struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	SessionID  uuid.UUID  `gorm:"type:uuid;not null" json:"session_id"`
	RunID      *uuid.UUID `gorm:"type:uuid" json:"run_id"`
	SessionSeq int64      `gorm:"not null" json:"session_seq"`
	Role       string     `gorm:"not null" json:"role"`
	Content    string     `gorm:"not null;default:''" json:"content"`
	// Parts 是按发生顺序保存的文本、思考与工具片段。
	Parts       datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"parts"`
	Status      string         `gorm:"not null" json:"status"`
	CreatedAt   time.Time      `gorm:"not null;default:now()" json:"created_at"`
	CompletedAt *time.Time     `gorm:"column:completed_at" json:"completed_at"`
}

// TableName 将 Message 映射到其数据库表。
func (Message) TableName() string { return "messages" }

// ToolCall 记录一次由模型请求的工具调用。
type ToolCall struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	RunID         uuid.UUID      `gorm:"type:uuid;not null" json:"run_id"`
	ApprovalID    *uuid.UUID     `gorm:"type:uuid" json:"approval_id"`
	Name          string         `gorm:"not null" json:"name"`
	ArgumentsJSON datatypes.JSON `gorm:"not null" json:"arguments_json"`
	Status        string         `gorm:"not null" json:"status"`
	ResultJSON    datatypes.JSON `gorm:"column:result_json" json:"result_json"`
	ErrorMessage  *string        `gorm:"column:error_message" json:"error_message"`
	CreatedAt     time.Time      `gorm:"not null;default:now()" json:"created_at"`
	StartedAt     *time.Time     `gorm:"column:started_at" json:"started_at"`
	FinishedAt    *time.Time     `gorm:"column:finished_at" json:"finished_at"`
}

// TableName 将 ToolCall 映射到其数据库表。
func (ToolCall) TableName() string { return "tool_calls" }

// Approval 是针对工具调用的待处理或已处理的人工决策。
type Approval struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	RunID                uuid.UUID      `gorm:"type:uuid;not null" json:"run_id"`
	ToolCallID           uuid.UUID      `gorm:"type:uuid;not null" json:"tool_call_id"`
	Status               string         `gorm:"not null" json:"status"`
	RequestedPayloadJSON datatypes.JSON `gorm:"not null" json:"requested_payload_json"`
	DecisionPayloadJSON  datatypes.JSON `json:"decision_payload_json"`
	RequestedAt          time.Time      `gorm:"not null;default:now()" json:"requested_at"`
	DecidedAt            *time.Time     `gorm:"column:decided_at" json:"decided_at"`
}

// TableName 将 Approval 映射到其数据库表。
func (Approval) TableName() string { return "approvals" }

// RunEvent 是属于某个 run 的有序事件。
type RunEvent struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey"`
	RunID       uuid.UUID      `gorm:"type:uuid;not null"`
	Seq         int64          `gorm:"not null"`
	Type        string         `gorm:"column:type;not null"`
	PayloadJSON datatypes.JSON `gorm:"not null"`
	CreatedAt   time.Time      `gorm:"not null;default:now()"`
}

// TableName 将 RunEvent 映射到其数据库表。
func (RunEvent) TableName() string { return "run_events" }
