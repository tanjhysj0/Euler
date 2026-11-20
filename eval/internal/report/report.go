// Package report 负责评测结果的读写与摘要输出。
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Meta 是 Runner 记录的每题元信息。
type Meta struct {
	Status           string `json:"status"`
	RunID            string `json:"run_id,omitempty"`
	DurationMS       int64  `json:"duration_ms"`
	ToolCalls        int    `json:"tool_calls"`
	Approvals        int    `json:"approvals"`
	DiffFiles        int    `json:"diff_files"`
	DiffAdded        int    `json:"diff_added"`
	DiffDeleted      int    `json:"diff_deleted"`
	TouchedTestFiles bool   `json:"touched_test_files"`
	Error            string `json:"error,omitempty"`
}

// Prediction 是 preds.jsonl 的一行。
type Prediction struct {
	InstanceID      string `json:"instance_id"`
	ModelNameOrPath string `json:"model_name_or_path"`
	ModelPatch      string `json:"model_patch"`
	Meta            Meta   `json:"meta"`
}

// TestSummary 是一组测试的通过情况。
type TestSummary struct {
	Total  int      `json:"total"`
	Passed int      `json:"passed"`
	Failed []string `json:"failed"`
}

// Metrics 是题目级工程指标。
type Metrics struct {
	DurationMS int64 `json:"duration_ms"`
	ToolCalls  int   `json:"tool_calls"`
	DiffFiles  int   `json:"diff_files"`
}

// TaskResult 是单题判分结果。
type TaskResult struct {
	InstanceID string      `json:"instance_id"`
	Resolved   bool        `json:"resolved"`
	Reason     string      `json:"reason,omitempty"`
	BuildOK    bool        `json:"build_ok"`
	FailToPass TestSummary `json:"fail_to_pass"`
	PassToPass TestSummary `json:"pass_to_pass"`
	Metrics    Metrics     `json:"metrics"`
}

// Totals 是聚合统计。
type Totals struct {
	Tasks        int     `json:"tasks"`
	Resolved     int     `json:"resolved"`
	ResolvedRate float64 `json:"resolved_rate"`
	PatchFailed  int     `json:"patch_failed"`
	NoChanges    int     `json:"no_changes"`
	BuildFailed  int     `json:"build_failed"`
	Error        int     `json:"error"`
	NoAction     int     `json:"no_action"`
}

// Report 是 report.json 的完整内容。
type Report struct {
	StartedAt  string       `json:"started_at"`
	FinishedAt string       `json:"finished_at"`
	Totals     Totals       `json:"totals"`
	Tasks      []TaskResult `json:"tasks"`
}

// WritePredictions 覆盖写入 preds.jsonl。
func WritePredictions(path string, preds []Prediction) error {
	file, err := os.Create(path) //nolint:gosec // 输出路径由评测器提供
	if err != nil {
		return fmt.Errorf("创建 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	writer := bufio.NewWriter(file)
	for _, pred := range preds {
		data, marshalErr := json.Marshal(pred)
		if marshalErr != nil {
			return marshalErr
		}
		if _, writeErr := writer.WriteString(string(data) + "\n"); writeErr != nil {
			return writeErr
		}
	}
	return writer.Flush()
}

// AppendPrediction 向 preds.jsonl 追加一行。
func AppendPrediction(path string, pred Prediction) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // 路径受信
	if err != nil {
		return fmt.Errorf("打开 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := json.Marshal(pred)
	if err != nil {
		return err
	}
	_, err = file.WriteString(string(data) + "\n")
	return err
}

// ReadPredictions 读取 preds.jsonl。
func ReadPredictions(path string) ([]Prediction, error) {
	file, err := os.Open(path) //nolint:gosec // 输入路径由评测器提供
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	preds := make([]Prediction, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var pred Prediction
		if err = json.Unmarshal([]byte(line), &pred); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", path, err)
		}
		preds = append(preds, pred)
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return preds, nil
}

// WriteReport 写入 report.json。
func WriteReport(path string, value Report) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // 报告文件按常规权限写入
}

// Summarize 汇总判分结果与 Runner 状态。
// 题目总数按 instance_id 去重统计，避免 preds 与 results 并存时重复计数。
func Summarize(results []TaskResult, preds []Prediction) Totals {
	ids := make(map[string]struct{}, len(results)+len(preds))
	for _, result := range results {
		ids[result.InstanceID] = struct{}{}
	}
	for _, pred := range preds {
		ids[pred.InstanceID] = struct{}{}
	}
	totals := Totals{Tasks: len(ids)}
	for _, result := range results {
		if result.Resolved {
			totals.Resolved++
			continue
		}
		switch result.Reason {
		case "patch_failed":
			totals.PatchFailed++
		case "build_failed":
			totals.BuildFailed++
		case "no_changes":
			totals.NoChanges++
		}
	}
	for _, pred := range preds {
		switch pred.Meta.Status {
		case "error":
			totals.Error++
		case "no_action":
			totals.NoAction++
		}
	}
	if totals.Tasks > 0 {
		totals.ResolvedRate = float64(totals.Resolved) / float64(totals.Tasks)
	}
	return totals
}

// PrintSummary 输出逐题摘要与控制台总计。
func PrintSummary(w io.Writer, value Report) {
	rows := make([]TaskResult, len(value.Tasks))
	copy(rows, value.Tasks)
	sort.Slice(rows, func(i, j int) bool { return rows[i].InstanceID < rows[j].InstanceID })
	for _, row := range rows {
		mark := "FAIL"
		if row.Resolved {
			mark = "PASS"
		}
		_, _ = fmt.Fprintf(w, "%-4s  %-20s  F2P %d/%d  P2P %d/%d  %dms  %d calls  %s\n",
			mark, row.InstanceID,
			row.FailToPass.Passed, row.FailToPass.Total,
			row.PassToPass.Passed, row.PassToPass.Total,
			row.Metrics.DurationMS, row.Metrics.ToolCalls,
			row.Reason)
	}
	_, _ = fmt.Fprintf(w, "总计: %d/%d resolved (%.1f%%)\n",
		value.Totals.Resolved, value.Totals.Tasks, value.Totals.ResolvedRate*100)
}
