package persistence

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Workspace is a normalized directory that sessions run against.
type Workspace struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Path      string    `gorm:"column:path;not null"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// TableName maps Workspace to its database table.
func (Workspace) TableName() string { return "workspaces" }

// ModelProfile points at an OpenAI compatible endpoint and a secret reference.
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

// TableName maps ModelProfile to its database table.
func (ModelProfile) TableName() string { return "model_profiles" }

// Session binds a workspace and a model profile to a conversation.
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

// TableName maps Session to its database table.
func (Session) TableName() string { return "sessions" }

// Run is one agent execution inside a session.
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

// TableName maps Run to its database table.
func (Run) TableName() string { return "runs" }

// Message is a user visible conversation message with a per-session sequence.
type Message struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	SessionID   uuid.UUID  `gorm:"type:uuid;not null" json:"session_id"`
	RunID       *uuid.UUID `gorm:"type:uuid" json:"run_id"`
	SessionSeq  int64      `gorm:"not null" json:"session_seq"`
	Role        string     `gorm:"not null" json:"role"`
	Content     string     `gorm:"not null;default:''" json:"content"`
	Status      string     `gorm:"not null" json:"status"`
	CreatedAt   time.Time  `gorm:"not null;default:now()" json:"created_at"`
	CompletedAt *time.Time `gorm:"column:completed_at" json:"completed_at"`
}

// TableName maps Message to its database table.
func (Message) TableName() string { return "messages" }

// ToolCall records one model requested tool invocation.
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

// TableName maps ToolCall to its database table.
func (ToolCall) TableName() string { return "tool_calls" }

// Approval is a pending or resolved human decision for a tool call.
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

// TableName maps Approval to its database table.
func (Approval) TableName() string { return "approvals" }

// RunEvent is an ordered event belonging to a run.
type RunEvent struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey"`
	RunID       uuid.UUID      `gorm:"type:uuid;not null"`
	Seq         int64          `gorm:"not null"`
	Type        string         `gorm:"column:type;not null"`
	PayloadJSON datatypes.JSON `gorm:"not null"`
	CreatedAt   time.Time      `gorm:"not null;default:now()"`
}

// TableName maps RunEvent to its database table.
func (RunEvent) TableName() string { return "run_events" }
