package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManagerNamesSessionAfterSuccessfulRun(t *testing.T) {
	store := newUnitFakeStore()
	titles := &unitFakeTitles{}
	sessionID := uuid.New()
	manager := NewManager(
		store, &unitFakeRunner{}, Limits{MaxDuration: time.Second}, Deps{Titles: titles},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, manager.Shutdown(ctx))
	})
	_, err := manager.Start(context.Background(), sessionID, "帮我修个 bug")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return len(titles.named()) == 1
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, []uuid.UUID{sessionID}, titles.named())
}

func TestManagerSkipsTitleOnFailedRun(t *testing.T) {
	store := newUnitFakeStore()
	titles := &unitFakeTitles{}
	runner := &unitFakeRunner{fn: func(context.Context) error {
		return errors.New("model exploded")
	}}
	manager := NewManager(
		store, runner, Limits{MaxDuration: time.Second}, Deps{Titles: titles},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, manager.Shutdown(ctx))
	})
	started, err := manager.Start(context.Background(), uuid.New(), "fail")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID && input.Status == StatusFailed {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
	require.Empty(t, titles.named())
}

// unitFakeTitles 记录 Manager 请求生成标题的会话。
type unitFakeTitles struct {
	mu      sync.Mutex
	targets []uuid.UUID
}

func (f *unitFakeTitles) NameSession(_ context.Context, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, sessionID)
	return nil
}

func (f *unitFakeTitles) named() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	targets := make([]uuid.UUID, len(f.targets))
	copy(targets, f.targets)
	return targets
}
