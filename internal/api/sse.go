package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/event"
)

const (
	sseContentType    = "text/event-stream"
	sseHeartbeatEvery = 15 * time.Second
	sseSequenceStep   = 1
	sseZero           = 0
	sseIntBase        = 10
	sseIntBits        = 64
)

// RunFinder 报告某个 run 是否存在。
type RunFinder interface {
	Exists(ctx context.Context, runID uuid.UUID) (bool, error)
}

// SSEHandler 先流式发送 run 的已持久化事件，再发送实时事件。
func SSEHandler(store event.Publisher, finder RunFinder) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID, ok := parseSSERunID(c)
		if !ok {
			return
		}
		if !runExists(c, finder, runID) {
			return
		}
		last := resolveLastSeq(c)
		live, cancel := store.Subscribe(runID)
		defer cancel()
		writeSSEHeaders(c)
		c.Writer.Flush()
		stream := &orderedStream{
			listAfter: func(ctx context.Context, after int64) ([]event.Event, error) {
				return store.ListAfter(ctx, runID, after)
			},
			live:      live,
			out:       c.Writer,
			flush:     c.Writer.Flush,
			heartbeat: sseHeartbeatEvery,
			last:      last,
		}
		if err := stream.run(c.Request.Context()); err != nil {
			slog.Debug("sse stream ended", "run_id", runID, "error", err)
		}
	}
}

// orderedStream 按序号顺序发送已持久化事件和实时事件，从存储补齐缺口，
// 并丢弃已发送过的实时事件。
type orderedStream struct {
	listAfter func(ctx context.Context, after int64) ([]event.Event, error)
	live      <-chan event.Event
	out       io.Writer
	flush     func()
	heartbeat time.Duration
	last      int64
}

func (s *orderedStream) run(ctx context.Context) error {
	if err := s.replay(ctx); err != nil {
		return err
	}
	ticker, beats := heartbeatTicker(s.heartbeat)
	if ticker != nil {
		defer ticker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case item, ok := <-s.live:
			if !ok {
				return nil
			}
			if err := s.handleLive(ctx, item); err != nil {
				return err
			}
		case <-beats:
			if _, err := io.WriteString(s.out, ": heartbeat\n\n"); err != nil {
				return err
			}
			s.flushOut()
		}
	}
}

func (s *orderedStream) replay(ctx context.Context) error {
	items, err := s.listAfter(ctx, s.last)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Seq <= s.last {
			continue
		}
		if err = s.emit(item); err != nil {
			return err
		}
	}
	return nil
}

func (s *orderedStream) handleLive(ctx context.Context, item event.Event) error {
	switch {
	case item.Seq <= s.last:
		return nil
	case item.Seq == s.last+sseSequenceStep:
		return s.emit(item)
	default:
		return s.fillGap(ctx, item)
	}
}

func (s *orderedStream) fillGap(ctx context.Context, item event.Event) error {
	if err := s.replay(ctx); err != nil {
		return err
	}
	if item.Seq <= s.last {
		return nil
	}
	return s.emit(item)
}

func (s *orderedStream) emit(item event.Event) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	_, err = fmt.Fprintf(s.out, "id: %d\ndata: %s\n\n", item.Seq, data)
	if err != nil {
		return err
	}
	s.last = item.Seq
	s.flushOut()
	return nil
}

func (s *orderedStream) flushOut() {
	if s.flush != nil {
		s.flush()
	}
}

func heartbeatTicker(interval time.Duration) (*time.Ticker, <-chan time.Time) {
	if interval <= sseZero {
		return nil, nil
	}
	ticker := time.NewTicker(interval)
	return ticker, ticker.C
}

func writeSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", sseContentType)
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
}

func parseSSERunID(c *gin.Context) (uuid.UUID, bool) {
	runID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		AbortWithError(c, http.StatusBadRequest, "invalid_request", "Run ID 无效")
		return uuid.Nil, false
	}
	return runID, true
}

func runExists(c *gin.Context, finder RunFinder, runID uuid.UUID) bool {
	exists, err := finder.Exists(c.Request.Context(), runID)
	if err != nil {
		AbortWithError(
			c, http.StatusInternalServerError, "internal_error", "读取 Run 失败",
		)
		return false
	}
	if !exists {
		AbortWithError(c, http.StatusNotFound, "not_found", "Run 不存在")
		return false
	}
	return true
}

func resolveLastSeq(c *gin.Context) int64 {
	after := parseAfterSeq(c)
	value, err := strconv.ParseInt(c.GetHeader("Last-Event-ID"), sseIntBase, sseIntBits)
	if err == nil && value > after {
		return value
	}
	return after
}

func parseAfterSeq(c *gin.Context) int64 {
	value, err := strconv.ParseInt(c.Query("after"), sseIntBase, sseIntBits)
	if err != nil || value < sseZero {
		return sseZero
	}
	return value
}
