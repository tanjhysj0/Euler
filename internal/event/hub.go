package event

import (
	"sync"

	"github.com/google/uuid"
)

// subscriberBufferSize 限定订阅者在被判定为慢订阅者并被丢弃之前，
// 最多可积压的待处理事件数量。
const (
	subscriberBufferSize = 64
	noSubscribers        = 0
)

// Hub 按 run 将事件扇出给进程内的订阅者。
type Hub struct {
	mu      sync.RWMutex
	streams map[uuid.UUID]map[chan Event]struct{}
}

// NewHub 构建一个空的 Hub。
func NewHub() *Hub {
	return &Hub{streams: make(map[uuid.UUID]map[chan Event]struct{})}
}

// Subscribe 为 runID 注册一个带缓冲的 channel。返回的 cancel 函数是幂等的，
// 会关闭该 channel。
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

// Publish 以非阻塞方式将 item 投递给每个在线订阅者。缓冲区已满的订阅者
// 会被关闭并移除，使其可以凭最后的序号从 PostgreSQL 重新接续。
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
