// Package event 持久化 run 事件并将其扇出给在线订阅者。
package event

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrRunNotFound 在向不存在的 run 追加事件时返回。
var ErrRunNotFound = errors.New("run not found")

// Event 是一条已持久化的 run 事件，在其 run 内按 Seq 排序。
type Event struct {
	ID        uuid.UUID       `json:"id"`
	RunID     uuid.UUID       `json:"run_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Publisher 持久化追加 run 事件，并按序号顺序流式推送。
type Publisher interface {
	Append(ctx context.Context, runID uuid.UUID, eventType string, payload any) (Event, error)
	ListAfter(ctx context.Context, runID uuid.UUID, after int64) ([]Event, error)
	Subscribe(runID uuid.UUID) (<-chan Event, func())
}
