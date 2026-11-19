package event

import (
	"sync"

	"github.com/google/uuid"
)

// subscriberBufferSize bounds how many pending events a subscriber may hold
// before it is considered slow and dropped.
const (
	subscriberBufferSize = 64
	noSubscribers        = 0
)

// Hub fans events out to in-process subscribers keyed by run.
type Hub struct {
	mu      sync.RWMutex
	streams map[uuid.UUID]map[chan Event]struct{}
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{streams: make(map[uuid.UUID]map[chan Event]struct{})}
}

// Subscribe registers a buffered channel for runID. The returned cancel
// function is idempotent and closes the channel.
func (h *Hub) Subscribe(runID uuid.UUID) (<-chan Event, func()) {
	events := make(chan Event, subscriberBufferSize)
	h.mu.Lock()
	subscribers, ok := h.streams[runID]
	if !ok {
		subscribers = make(map[chan Event]struct{})
		h.streams[runID] = subscribers
	}
	subscribers[events] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.remove(runID, events)
		})
	}
	return events, cancel
}

// Publish delivers item to every live subscriber without blocking. A
// subscriber whose buffer is full is closed and removed so it can reconnect
// from PostgreSQL using its last sequence.
func (h *Hub) Publish(item Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subscribers := h.streams[item.RunID]
	for events := range subscribers {
		select {
		case events <- item:
		default:
			delete(subscribers, events)
			close(events)
		}
	}
	if len(subscribers) == noSubscribers {
		delete(h.streams, item.RunID)
	}
}

func (h *Hub) remove(runID uuid.UUID, events chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subscribers, ok := h.streams[runID]
	if !ok {
		return
	}
	if _, exists := subscribers[events]; !exists {
		return
	}
	delete(subscribers, events)
	close(events)
	if len(subscribers) == noSubscribers {
		delete(h.streams, runID)
	}
}
