// Package event persists run events and fans them out to live subscribers.
package event

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrRunNotFound is returned when appending an event to an unknown run.
var ErrRunNotFound = errors.New("run not found")

// Event is one persisted run event, ordered by Seq within its run.
type Event struct {
	ID        uuid.UUID       `json:"id"`
	RunID     uuid.UUID       `json:"run_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Publisher durably appends run events and streams them in sequence order.
type Publisher interface {
	Append(ctx context.Context, runID uuid.UUID, eventType string, payload any) (Event, error)
	ListAfter(ctx context.Context, runID uuid.UUID, after int64) ([]Event, error)
	Subscribe(runID uuid.UUID) (<-chan Event, func())
}
