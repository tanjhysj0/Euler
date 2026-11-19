package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/event"
)

type stubRunFinder struct {
	exists bool
	err    error
}

func (s stubRunFinder) Exists(_ context.Context, _ uuid.UUID) (bool, error) {
	return s.exists, s.err
}

type fakeStore struct {
	mu             sync.Mutex
	persisted      []event.Event
	live           chan event.Event
	cancelled      chan struct{}
	cancelOnce     sync.Once
	listErr        error
	firstCallLimit int64
	listCalls      int
}

var _ event.Publisher = (*fakeStore)(nil)

func (f *fakeStore) Append(
	_ context.Context, runID uuid.UUID, eventType string, payload any,
) (event.Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return event.Event{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	item := event.Event{
		ID:        uuid.New(),
		RunID:     runID,
		Seq:       int64(len(f.persisted) + 1),
		Type:      eventType,
		Payload:   raw,
		CreatedAt: time.Now(),
	}
	f.persisted = append(f.persisted, item)
	return item, nil
}

func (f *fakeStore) ListAfter(
	_ context.Context, _ uuid.UUID, after int64,
) ([]event.Event, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	limit := f.firstCallLimit
	f.listCalls++
	out := make([]event.Event, 0, len(f.persisted))
	for _, item := range f.persisted {
		if item.Seq <= after {
			continue
		}
		if f.listCalls == 1 && limit > 0 && item.Seq > limit {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeStore) Subscribe(_ uuid.UUID) (<-chan event.Event, func()) {
	return f.live, func() {
		f.cancelOnce.Do(func() { close(f.cancelled) })
	}
}

type sseFixture struct {
	t     *testing.T
	runID uuid.UUID
	store *fakeStore
}

func newSSEFixture(t *testing.T) *sseFixture {
	t.Helper()
	return &sseFixture{
		t:     t,
		runID: uuid.New(),
		store: &fakeStore{
			live:      make(chan event.Event, 16),
			cancelled: make(chan struct{}),
		},
	}
}

func (f *sseFixture) event(seq int64) event.Event {
	return event.Event{
		ID:        uuid.New(),
		RunID:     f.runID,
		Seq:       seq,
		Type:      "message.delta",
		Payload:   json.RawMessage(fmt.Sprintf(`{"seq":%d}`, seq)),
		CreatedAt: time.Now(),
	}
}

func (f *sseFixture) persistEvents(sequences ...int64) {
	for _, seq := range sequences {
		f.store.persisted = append(f.store.persisted, f.event(seq))
	}
}

func TestOrderedStreamFillsLiveGap(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1, 2, 3)
	live := make(chan event.Event, 3)
	live <- fixture.event(3)
	live <- fixture.event(2)
	out := collectOrderedEvents(t, fixture.store, live, 3)
	require.Equal(t, []int64{1, 2, 3}, eventSequences(out))
}

func TestOrderedStreamFillsGapFromStore(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1, 2, 3)
	fixture.store.firstCallLimit = 1
	live := make(chan event.Event, 2)
	live <- fixture.event(3)
	out := collectOrderedEvents(t, fixture.store, live, 3)
	require.Equal(t, []int64{1, 2, 3}, eventSequences(out))
}

func TestOrderedStreamDropsDuplicateLiveEvents(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1, 2)
	live := make(chan event.Event, 2)
	live <- fixture.event(2)
	live <- fixture.event(2)
	out := collectOrderedEvents(t, fixture.store, live, 2)
	require.Equal(t, []int64{1, 2}, eventSequences(out))
}

func TestOrderedStreamWritesHeartbeat(t *testing.T) {
	recorder := &eventRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &orderedStream{
		listAfter: func(context.Context, int64) ([]event.Event, error) { return nil, nil },
		live:      make(chan event.Event),
		out:       recorder,
		heartbeat: 5 * time.Millisecond,
	}
	finished := make(chan error, 1)
	go func() { finished <- stream.run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(recorder.snapshot(), ": heartbeat") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for heartbeat: %q", recorder.snapshot())
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	require.NoError(t, <-finished)
}

func TestSSEHandlerRejectsInvalidRunID(t *testing.T) {
	fixture := newSSEFixture(t)
	server := newSSEHandlerServer(t, fixture, stubRunFinder{exists: true})
	response, err := http.Get(server.URL + "/runs/not-a-uuid/events")
	require.NoError(t, err)
	defer closeBody(t, response.Body)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "invalid_request", decodeBody(t, readAll(t, response.Body))["code"])
}

func TestSSEHandlerRejectsMissingRun(t *testing.T) {
	fixture := newSSEFixture(t)
	server := newSSEHandlerServer(t, fixture, stubRunFinder{exists: false})
	response, err := http.Get(server.URL + "/runs/" + fixture.runID.String() + "/events")
	require.NoError(t, err)
	defer closeBody(t, response.Body)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.Equal(t, "not_found", decodeBody(t, readAll(t, response.Body))["code"])
}

func TestSSEHandlerReportsFinderFailure(t *testing.T) {
	fixture := newSSEFixture(t)
	finder := stubRunFinder{err: errors.New("database unavailable")}
	server := newSSEHandlerServer(t, fixture, finder)
	response, err := http.Get(server.URL + "/runs/" + fixture.runID.String() + "/events")
	require.NoError(t, err)
	defer closeBody(t, response.Body)
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.Equal(t, "internal_error", decodeBody(t, readAll(t, response.Body))["code"])
}

func TestSSEHandlerStreamsReplayThenLive(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1, 2)
	fixture.store.live <- fixture.event(2)
	fixture.store.live <- fixture.event(3)
	server := newSSEHandlerServer(t, fixture, stubRunFinder{exists: true})
	response, err := http.Get(server.URL + "/runs/" + fixture.runID.String() + "/events")
	require.NoError(t, err)
	defer closeBody(t, response.Body)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	require.Equal(t, "no-cache", response.Header.Get("Cache-Control"))
	read := readSSEEvents(t, response.Body, 3)
	require.Equal(t, []int64{1, 2, 3}, read.ids)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(read.data[0]), &payload))
	for _, key := range []string{"run_id", "seq", "type", "payload"} {
		require.Contains(t, payload, key)
	}
	require.Equal(t, fixture.runID.String(), payload["run_id"])
}

func TestSSEHandlerHonorsLastEventID(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1, 2, 3)
	server := newSSEHandlerServer(t, fixture, stubRunFinder{exists: true})
	request, err := http.NewRequest(
		http.MethodGet, server.URL+"/runs/"+fixture.runID.String()+"/events", nil,
	)
	require.NoError(t, err)
	request.Header.Set("Last-Event-ID", "2")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer closeBody(t, response.Body)
	read := readSSEEvents(t, response.Body, 1)
	require.Equal(t, []int64{3}, read.ids)
}

func TestSSEHandlerCancelsSubscriptionOnDisconnect(t *testing.T) {
	fixture := newSSEFixture(t)
	fixture.persistEvents(1)
	server := newSSEHandlerServer(t, fixture, stubRunFinder{exists: true})
	response, err := http.Get(server.URL + "/runs/" + fixture.runID.String() + "/events")
	require.NoError(t, err)
	read := readSSEEvents(t, response.Body, 1)
	require.Equal(t, []int64{1}, read.ids)
	require.NoError(t, response.Body.Close())
	select {
	case <-fixture.store.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription was not cancelled after client disconnect")
	}
}

type sseRead struct {
	ids  []int64
	data []string
}

func readSSEEvents(t *testing.T, body io.Reader, want int) sseRead {
	t.Helper()
	results := make(chan sseRead, 1)
	go func() {
		reader := bufio.NewReader(body)
		read := sseRead{}
		for len(read.ids) < want {
			line, err := reader.ReadString('\n')
			if err != nil {
				results <- read
				return
			}
			if strings.HasPrefix(line, "id: ") {
				seq, parseErr := strconv.ParseInt(strings.TrimSpace(line[4:]), 10, 64)
				if parseErr == nil {
					read.ids = append(read.ids, seq)
				}
			} else if strings.HasPrefix(line, "data: ") {
				read.data = append(read.data, strings.TrimSpace(line[6:]))
			}
		}
		results <- read
	}()
	select {
	case read := <-results:
		require.Len(t, read.ids, want)
		return read
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading SSE events")
		return sseRead{}
	}
}

func newSSEHandlerServer(t *testing.T, fixture *sseFixture, finder RunFinder) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/runs/:id/events", SSEHandler(fixture.store, finder))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func closeBody(t *testing.T, body io.Closer) {
	t.Helper()
	require.NoError(t, body.Close())
}

func readAll(t *testing.T, body io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	return data
}

type eventRecorder struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (r *eventRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.Write(data)
}

func (r *eventRecorder) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.String()
}

func (r *eventRecorder) events(t *testing.T) []event.Event {
	t.Helper()
	out := make([]event.Event, 0, 4)
	for _, block := range strings.Split(r.snapshot(), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var item event.Event
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &item))
			out = append(out, item)
		}
	}
	return out
}

func collectOrderedEvents(
	t *testing.T, store event.Publisher, live <-chan event.Event, want int,
) []event.Event {
	t.Helper()
	recorder := &eventRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &orderedStream{
		listAfter: func(ctx context.Context, after int64) ([]event.Event, error) {
			return store.ListAfter(ctx, uuid.Nil, after)
		},
		live: live,
		out:  recorder,
	}
	finished := make(chan error, 1)
	go func() { finished <- stream.run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(recorder.events(t)) < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d events, got %q", want, recorder.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	require.NoError(t, <-finished)
	return recorder.events(t)
}

func eventSequences(events []event.Event) []int64 {
	out := make([]int64, len(events))
	for index, item := range events {
		out[index] = item.Seq
	}
	return out
}
