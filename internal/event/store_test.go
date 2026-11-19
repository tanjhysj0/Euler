package event

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHubPublishesToAllSubscribers(t *testing.T) {
	hub := NewHub()
	runID := uuid.New()
	first, cancelFirst := hub.Subscribe(runID)
	defer cancelFirst()
	second, cancelSecond := hub.Subscribe(runID)
	defer cancelSecond()
	item := Event{ID: uuid.New(), RunID: runID, Seq: 1, Type: "run.started"}
	hub.Publish(item)
	require.Equal(t, int64(1), receive(t, first).Seq)
	require.Equal(t, int64(1), receive(t, second).Seq)
}

func TestHubIgnoresOtherRuns(t *testing.T) {
	hub := NewHub()
	runID := uuid.New()
	events, cancel := hub.Subscribe(runID)
	defer cancel()
	hub.Publish(Event{ID: uuid.New(), RunID: uuid.New(), Seq: 1, Type: "run.started"})
	select {
	case item := <-events:
		t.Fatalf("unexpected event for other run: %+v", item)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	hub := NewHub()
	runID := uuid.New()
	events, cancel := hub.Subscribe(runID)
	defer cancel()
	for seq := int64(1); seq <= int64(subscriberBufferSize)+1; seq++ {
		hub.Publish(Event{ID: uuid.New(), RunID: runID, Seq: seq})
	}
	received := 0
	for range events {
		received++
	}
	require.Equal(t, subscriberBufferSize, received)
}

func TestHubCancelIsIdempotent(t *testing.T) {
	hub := NewHub()
	runID := uuid.New()
	events, cancel := hub.Subscribe(runID)
	cancel()
	cancel()
	_, open := <-events
	require.False(t, open)
	require.NotPanics(t, func() {
		hub.Publish(Event{ID: uuid.New(), RunID: runID, Seq: 1})
	})
}

func receive(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case item := <-events:
		return item
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}
