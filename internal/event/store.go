package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"euler/internal/persistence"
)

// Store is the PostgreSQL backed event store.
type Store struct {
	db  *gorm.DB
	hub *Hub
}

var _ Publisher = (*Store)(nil)

// NewStore builds a Store that publishes committed events to hub.
func NewStore(db *gorm.DB, hub *Hub) *Store {
	return &Store{db: db, hub: hub}
}

// Append persists one event at the run's next sequence and then publishes the
// committed row to live subscribers.
func (s *Store) Append(
	ctx context.Context, runID uuid.UUID, eventType string, payload any,
) (Event, error) {
	var stored Event
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var txErr error
		stored, txErr = AppendTx(tx, runID, eventType, payload)
		return txErr
	})
	if err != nil {
		return Event{}, err
	}
	s.Publish(stored)
	return stored, nil
}

// AppendTx inserts the event inside tx; publish after the caller commits.
func AppendTx(
	tx *gorm.DB, runID uuid.UUID, eventType string, payload any,
) (Event, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}
	var stored Event
	pending := pendingEvent{runID: runID, eventType: eventType, payload: payloadJSON}
	if err = appendPending(tx, pending, &stored); err != nil {
		return Event{}, err
	}
	return stored, nil
}

// Publish delivers a committed event to live subscribers.
func (s *Store) Publish(e Event) {
	s.hub.Publish(e)
}

type pendingEvent struct {
	runID     uuid.UUID
	eventType string
	payload   []byte
}

func appendPending(tx *gorm.DB, pending pendingEvent, stored *Event) error {
	var run persistence.Run
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "next_event_seq").
		First(&run, "id = ?", pending.runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	row := persistence.RunEvent{
		ID:          uuid.New(),
		RunID:       pending.runID,
		Seq:         run.NextEventSeq,
		Type:        pending.eventType,
		PayloadJSON: datatypes.JSON(pending.payload),
		CreatedAt:   time.Now(),
	}
	if err = tx.Create(&row).Error; err != nil {
		return fmt.Errorf("insert run event: %w", err)
	}
	update := tx.Model(&persistence.Run{}).Where("id = ?", pending.runID).
		Update("next_event_seq", gorm.Expr("next_event_seq + 1"))
	if err = update.Error; err != nil {
		return fmt.Errorf("advance run sequence: %w", err)
	}
	*stored = Event{
		ID:        row.ID,
		RunID:     row.RunID,
		Seq:       row.Seq,
		Type:      row.Type,
		Payload:   json.RawMessage(pending.payload),
		CreatedAt: row.CreatedAt,
	}
	return nil
}

// ListAfter returns the run's events with a sequence greater than after in
// ascending order.
func (s *Store) ListAfter(
	ctx context.Context, runID uuid.UUID, after int64,
) ([]Event, error) {
	var rows []persistence.RunEvent
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND seq > ?", runID, after).
		Order("seq ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
	}
	var events []Event
	for _, row := range rows {
		events = append(events, Event{
			ID:        row.ID,
			RunID:     row.RunID,
			Seq:       row.Seq,
			Type:      row.Type,
			Payload:   json.RawMessage(row.PayloadJSON),
			CreatedAt: row.CreatedAt,
		})
	}
	return events, nil
}

// Subscribe registers a live subscription for runID.
func (s *Store) Subscribe(runID uuid.UUID) (<-chan Event, func()) {
	return s.hub.Subscribe(runID)
}
