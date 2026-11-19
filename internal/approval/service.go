// Package approval gates sensitive tool execution behind an explicit human
// decision: the run parks in waiting_approval until a decision is recorded.
package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"euler/internal/event"
	"euler/internal/run"
)

const (
	// StatusPending marks an approval that is waiting for a decision.
	StatusPending = "pending"
	// StatusApproved marks a decision that lets the tool call proceed.
	StatusApproved = "approved"
	// StatusRejected marks a decision that denies the tool call.
	StatusRejected = "rejected"
	// StatusCancelled marks an approval closed because its run stopped waiting.
	StatusCancelled = "cancelled"

	// EventRequested names the approval creation event.
	EventRequested = "approval.requested"
	// EventResolved names the approval decision event.
	EventResolved = "approval.resolved"

	// keyApprovalID is the approval id field of event payloads.
	keyApprovalID = "approval_id"
	// keyToolCallID is the tool call id field of event payloads.
	keyToolCallID = "tool_call_id"
	// keyName is the tool name field of event payloads.
	keyName = "name"
	// keyRequest is the reviewed tool arguments field of the request event.
	keyRequest = "request"
	// keyStatus is the approval status field of the resolved event.
	keyStatus = "status"
	// keyApproved is the decision outcome field of event payloads.
	keyApproved = "approved"
	// keyReason is the decision reason field of event payloads.
	keyReason = "reason"

	// decisionBuffer lets a decision land while Require is still publishing.
	decisionBuffer = 1
)

var (
	// ErrAlreadyDecided rejects a second decision for one approval.
	ErrAlreadyDecided = errors.New("approval already decided")
	// ErrNotFound marks an unknown approval id.
	ErrNotFound = errors.New("approval not found")
	// ErrInvalidRequest marks an approval request without run or tool call.
	ErrInvalidRequest = errors.New("invalid approval request")
)

// Gate blocks a tool call until a human decision is recorded. Service is the
// production implementation; tool registration in the agent loop depends only
// on this interface.
type Gate interface {
	Require(ctx context.Context, request Request) (Decision, error)
}

// Decision is the outcome one waiter observes for one approval.
type Decision struct {
	Approved bool   `json:"approved"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Request describes the tool call that needs approval. Service assigns ID, so
// callers leave it zero; the durable row and the waiter registry share it.
type Request struct {
	ID         uuid.UUID      `json:"id"`
	RunID      uuid.UUID      `json:"run_id"`
	ToolCallID uuid.UUID      `json:"tool_call_id"`
	Name       string         `json:"name"`
	Payload    map[string]any `json:"payload"`
}

// Resolution is one decision as written to the store.
type Resolution struct {
	Status   string
	Approved bool
	Reason   string
}

// Record is the durable view of one approval.
type Record struct {
	ID          uuid.UUID  `json:"id"`
	RunID       uuid.UUID  `json:"run_id"`
	ToolCallID  uuid.UUID  `json:"tool_call_id"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	Decision    Decision   `json:"decision"`
}

// Store persists approvals together with the run and tool call statuses they
// own. Implementations write state and its event in one database transaction
// and return that event, which the caller publishes only after the commit.
type Store interface {
	// Open writes the pending approval and parks the run in waiting_approval.
	Open(ctx context.Context, request Request) (event.Event, error)
	// Resolve records the first decision for id, or ErrAlreadyDecided.
	Resolve(ctx context.Context, id uuid.UUID, resolution Resolution) (Record, event.Event, error)
	// Get loads one approval.
	Get(ctx context.Context, id uuid.UUID) (Record, error)
}

// Broadcaster delivers committed events to live subscribers. *event.Store
// satisfies it; appending stays in Store so ordering stays transactional.
type Broadcaster interface {
	Publish(committed event.Event)
}

// Service implements Gate over a durable Store and an in-memory waiter table.
type Service struct {
	store   Store
	events  Broadcaster
	mu      sync.Mutex
	waiters map[uuid.UUID]chan Decision
}

// NewService wires an approval service.
func NewService(store Store, events Broadcaster) *Service {
	return &Service{
		store:   store,
		events:  events,
		waiters: make(map[uuid.UUID]chan Decision),
	}
}

var _ Gate = (*Service)(nil)

// Require persists a pending approval, publishes approval.requested and blocks
// until a decision arrives. A cancelled context cancels the approval and its
// tool call and returns the context error; a decision that won the race is
// returned instead.
func (s *Service) Require(ctx context.Context, request Request) (Decision, error) {
	if err := validate(request); err != nil {
		return Decision{}, err
	}
	request.ID = uuid.New()
	waiter := s.watch(request.ID)
	created, err := s.store.Open(ctx, request)
	if err != nil {
		s.stop(request.ID)
		return Decision{}, err
	}
	s.events.Publish(created)
	return s.await(ctx, request.ID, waiter)
}

// Decide records the decision for id. The first call wins: state, event and
// delivery happen in that order, and a second call returns ErrAlreadyDecided
// without touching the database.
func (s *Service) Decide(
	ctx context.Context,
	id uuid.UUID,
	resolution Resolution,
) (Record, error) {
	record, resolved, err := s.store.Resolve(ctx, id, resolution)
	if err != nil {
		return Record{}, err
	}
	s.events.Publish(resolved)
	s.deliver(record.ID, record.Decision)
	return record, nil
}

// Get loads one approval for status polling and tests.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Record, error) {
	return s.store.Get(ctx, id)
}

// await blocks on the decision channel and the caller context.
func (s *Service) await(
	ctx context.Context,
	id uuid.UUID,
	waiter chan Decision,
) (Decision, error) {
	select {
	case decision := <-waiter:
		s.stop(id)
		return decision, nil
	case <-ctx.Done():
		return s.abandon(ctx, id)
	}
}

// abandon closes an approval nobody waits for anymore. It re-checks the
// channel while removing it, so a decision recorded in the same instant is
// returned instead of being overwritten by a cancellation.
func (s *Service) abandon(ctx context.Context, id uuid.UUID) (Decision, error) {
	decision, settled := s.reclaim(id)
	if settled {
		return decision, nil
	}
	resolution := Resolution{Status: StatusCancelled}
	record, resolved, err := s.store.Resolve(
		context.WithoutCancel(ctx), id, resolution)
	switch {
	case errors.Is(err, ErrAlreadyDecided):
		// The deciding path already published approval.resolved.
		return record.Decision, nil
	case err != nil:
		return Decision{}, errors.Join(ctx.Err(), err)
	}
	s.events.Publish(resolved)
	return Decision{Status: StatusCancelled}, ctx.Err()
}

func validate(request Request) error {
	switch {
	case request.RunID == uuid.Nil, request.ToolCallID == uuid.Nil:
		return ErrInvalidRequest
	case request.Name == "":
		return ErrInvalidRequest
	default:
		return nil
	}
}

// watch registers the waiter channel for id before anything is durable, so a
// decision can never be delivered to a table entry that does not exist yet.
func (s *Service) watch(id uuid.UUID) chan Decision {
	waiter := make(chan Decision, decisionBuffer)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiters[id] = waiter
	return waiter
}

// stop unregisters the waiter channel for id.
func (s *Service) stop(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiters, id)
}

// reclaim unregisters id and reports a decision that arrived first.
func (s *Service) reclaim(id uuid.UUID) (Decision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter, ok := s.waiters[id]
	if !ok {
		return Decision{}, false
	}
	delete(s.waiters, id)
	select {
	case decision := <-waiter:
		return decision, true
	default:
		return Decision{}, false
	}
}

// deliver hands the durable decision to the registered waiter, if any. One
// approval id is decided at most once, so the buffered send never blocks.
func (s *Service) deliver(id uuid.UUID, decision Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter, ok := s.waiters[id]
	if !ok {
		return
	}
	select {
	case waiter <- decision:
	default:
		return
	}
}

// requestedPayload renders the event body of approval.requested.
func requestedPayload(request Request) map[string]any {
	payload := request.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{
		keyApprovalID: request.ID.String(),
		keyToolCallID: request.ToolCallID.String(),
		keyName:       request.Name,
		keyRequest:    payload,
	}
}

// resolvedPayload renders the event body of approval.resolved.
func resolvedPayload(
	id uuid.UUID,
	toolCallID uuid.UUID,
	resolution Resolution,
) map[string]any {
	return map[string]any{
		keyApprovalID: id.String(),
		keyToolCallID: toolCallID.String(),
		keyStatus:     resolution.Status,
		keyApproved:   resolution.Approved,
		keyReason:     resolution.Reason,
	}
}

// runStatusChange parks or releases one run around an approval.
func runStatusChange(id uuid.UUID, from, to string) run.StatusChange {
	return run.StatusChange{RunID: id, From: from, To: to}
}
