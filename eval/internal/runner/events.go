package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	maxReconnect    = 3
	pollInterval    = 2 * time.Second
	eventDataPrefix = "data: "
)

type event struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type approvalPayload struct {
	ApprovalID string `json:"approval_id"`
}

type runMetrics struct {
	toolCalls int
	approvals int
}

// follow 消费事件直到终态，返回终态事件名与指标。
func (c *Client) follow(ctx context.Context, runID string) (string, runMetrics, error) {
	var metrics runMetrics
	var after int64
	for attempt := 0; attempt <= maxReconnect; attempt++ {
		terminal, last, err := c.consume(ctx, runID, after, &metrics)
		if terminal != "" {
			return terminal, metrics, nil
		}
		if ctx.Err() != nil {
			return "", metrics, ctx.Err()
		}
		after = last
		if err != nil && attempt == maxReconnect {
			terminal, pollErr := c.poll(ctx, runID, &metrics)
			return terminal, metrics, pollErr
		}
	}
	return "", metrics, fmt.Errorf("事件流异常结束")
}

// consume 读取一次 SSE 连接。
func (c *Client) consume(
	ctx context.Context, runID string, after int64, metrics *runMetrics,
) (string, int64, error) {
	url := fmt.Sprintf("%s/api/v1/runs/%s/events?after=%d", c.baseURL, runID, after)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", after, err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", after, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", after, fmt.Errorf("订阅事件失败: HTTP %d", response.StatusCode)
	}
	reader := bufio.NewReaderSize(response.Body, 64*1024)
	last := after
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			if ctx.Err() != nil {
				return "", last, ctx.Err()
			}
			return "", last, readErr
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, eventDataPrefix) {
			continue
		}
		var item event
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, eventDataPrefix)), &item); err != nil {
			continue
		}
		if item.Seq > last {
			last = item.Seq
		}
		switch item.Type {
		case "approval.requested":
			metrics.approvals++
			var payload approvalPayload
			if err = json.Unmarshal(item.Payload, &payload); err == nil && payload.ApprovalID != "" {
				c.approve(payload.ApprovalID)
			}
		case "run.completed", "run.failed", "run.cancelled", "run.interrupted":
			snap, snapErr := c.snapshot(ctx, runID)
			if snapErr == nil {
				metrics.toolCalls = len(snap.ToolCalls)
			}
			return item.Type, last, nil
		case "tool.completed", "tool.failed":
			metrics.toolCalls++
		}
	}
}

// approve 立即批准一次工具调用审批。
func (c *Client) approve(approvalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	body := map[string]any{"approved": true, "reason": autoApproveReason}
	_ = c.doJSON(ctx, http.MethodPost,
		"/api/v1/approvals/"+approvalID+"/decision", body, nil)
}

// poll 是 SSE 失败后的兜底轮询。
func (c *Client) poll(ctx context.Context, runID string, metrics *runMetrics) (string, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		snap, err := c.snapshot(ctx, runID)
		if err != nil {
			return "", err
		}
		metrics.toolCalls = len(snap.ToolCalls)
		metrics.approvals = len(snap.Approvals)
		switch snap.Run.Status {
		case "succeeded":
			return "run.completed", nil
		case "failed":
			return "run.failed", nil
		case "cancelled":
			return "run.cancelled", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
