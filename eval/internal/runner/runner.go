// Package runner 通过 HTTP 驱动 Euler 服务完成编码任务。
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"euler/eval/internal/report"
	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

const (
	defaultRunTimeout = 15 * time.Minute
	healthRetryEvery  = 500 * time.Millisecond
	healthRetryTotal  = 30 * time.Second
	cancelTimeout     = 10 * time.Second
	maxPatchBytes     = 2 * 1024 * 1024
	statusCompleted   = "completed"
	statusFailed      = "failed"
	statusTimeout     = "timeout"
	statusError       = "error"
	statusPatchTooBig = "patch_too_large"
	autoApproveReason = "euleval auto-approve"
)

// Options 是连接 Euler 服务所需的配置。
type Options struct {
	BaseURL      string
	ProfileID    string
	ModelBaseURL string
	Model        string
	APIKeyEnv    string
	ModelName    string
	RunTimeout   time.Duration
}

// Client 是 Euler API 的轻量客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
	profileID  string
	modelName  string
	runTimeout time.Duration
}

// NewClient 完成健康检查、cookie 获取与模型配置准备。
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &Client{
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		httpClient: &http.Client{Jar: jar},
		profileID:  opts.ProfileID,
		modelName:  opts.ModelName,
		runTimeout: opts.RunTimeout,
	}
	if client.runTimeout <= 0 {
		client.runTimeout = defaultRunTimeout
	}
	if err = client.waitHealthy(ctx); err != nil {
		return nil, err
	}
	if err = client.bootstrap(ctx); err != nil {
		return nil, err
	}
	if client.profileID == "" {
		if err = client.createProfile(ctx, opts); err != nil {
			return nil, err
		}
	}
	if client.modelName == "" {
		client.modelName = "euler-local"
	}
	return client, nil
}

func (c *Client) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(healthRetryTotal)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
		if err != nil {
			return err
		}
		response, err := c.httpClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("euler 服务未就绪: %s", c.baseURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthRetryEvery):
		}
	}
}

func (c *Client) bootstrap(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, c.baseURL+"/api/v1/bootstrap", nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("获取本机 cookie: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("获取本机 cookie 失败: HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) createProfile(ctx context.Context, opts Options) error {
	if opts.ModelBaseURL == "" || opts.Model == "" || opts.APIKeyEnv == "" {
		return errors.New("需要 --profile-id 或同时提供 --model-base-url/--model/--api-key-env")
	}
	body := map[string]string{
		"name":        fmt.Sprintf("euleval-%d", time.Now().Unix()),
		"base_url":    opts.ModelBaseURL,
		"model":       opts.Model,
		"secret_kind": "environment",
		"secret_ref":  opts.APIKeyEnv,
	}
	var created struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/model-profiles", body, &created); err != nil {
		return err
	}
	c.profileID = created.ID
	if c.modelName == "" {
		c.modelName = created.Model
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, response.StatusCode,
			strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// RunTask 执行单题并返回一条 prediction。
func (c *Client) RunTask(ctx context.Context, t task.Task, repo *workspace.Repo) report.Prediction {
	pred := report.Prediction{
		InstanceID:      t.ID,
		ModelNameOrPath: c.modelName,
		Meta: report.Meta{
			Status:     statusError,
			DurationMS: 0,
		},
	}
	started := time.Now()
	defer func() { pred.Meta.DurationMS = time.Since(started).Milliseconds() }()

	var created struct {
		ID string `json:"id"`
	}
	createBody := map[string]string{"profile_id": c.profileID, "path": repo.Dir}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/sessions", createBody, &created); err != nil {
		pred.Meta.Error = err.Error()
		return pred
	}
	var startedRun struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	runBody := map[string]string{"content": t.Issue}
	if err := c.doJSON(ctx, http.MethodPost,
		"/api/v1/sessions/"+created.ID+"/runs", runBody, &startedRun); err != nil {
		pred.Meta.Error = err.Error()
		return pred
	}
	pred.Meta.RunID = startedRun.Run.ID

	runCtx, cancel := context.WithTimeout(ctx, c.runTimeout)
	defer cancel()
	terminal, metrics, err := c.follow(runCtx, startedRun.Run.ID)
	pred.Meta.ToolCalls = metrics.toolCalls
	pred.Meta.Approvals = metrics.approvals
	switch {
	case runCtx.Err() != nil:
		c.cancelRun(startedRun.Run.ID)
		pred.Meta.Status = statusTimeout
		pred.Meta.Error = "运行超时"
	case err != nil:
		pred.Meta.Status = statusFailed
		pred.Meta.Error = err.Error()
	case terminal != "run.completed":
		pred.Meta.Status = statusFailed
		pred.Meta.Error = terminal
	default:
		pred.Meta.Status = statusCompleted
	}
	patch, patchErr := repo.ExtractPatch()
	if patchErr != nil {
		pred.Meta.Status = statusError
		pred.Meta.Error = patchErr.Error()
		return pred
	}
	if len(patch) > maxPatchBytes {
		pred.Meta.Status = statusPatchTooBig
		return pred
	}
	pred.ModelPatch = patch
	files, added, deleted, touched := DiffStats(patch)
	pred.Meta.DiffFiles = files
	pred.Meta.DiffAdded = added
	pred.Meta.DiffDeleted = deleted
	pred.Meta.TouchedTestFiles = touched
	if strings.TrimSpace(patch) == "" && pred.Meta.Status == statusCompleted {
		pred.Meta.Status = statusCompleted
	}
	return pred
}

func (c *Client) cancelRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	_ = c.doJSON(ctx, http.MethodPost, "/api/v1/runs/"+runID+"/cancel", nil, nil)
}

func (c *Client) snapshot(ctx context.Context, runID string) (runSnapshot, error) {
	var snap runSnapshot
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/runs/"+runID, nil, &snap)
	return snap, err
}

type runSnapshot struct {
	Run struct {
		Status string `json:"status"`
	} `json:"run"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
	Approvals []json.RawMessage `json:"approvals"`
}

// DiffStats 统计补丁的文件数与增删行数。
func DiffStats(patch string) (files, added, deleted int, touchedTest bool) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+++ b/"):
			if strings.HasSuffix(line, "_test.go") {
				touchedTest = true
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deleted++
		}
	}
	return files, added, deleted, touchedTest
}
