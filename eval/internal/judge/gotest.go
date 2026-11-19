package judge

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"euler/eval/internal/report"
)

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type testOutcomes struct {
	results map[string]string
}

// parseTestOutput 解析 go test -json 流，按 "包::测试" 记录状态。
func parseTestOutput(reader io.Reader) testOutcomes {
	outcomes := testOutcomes{results: make(map[string]string)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Test == "" || event.Action == "" {
			continue
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		outcomes.results[event.Package+"::"+event.Test] = event.Action
	}
	return outcomes
}

// lookup 支持 "TestX" 与 "包路径::TestX" 两种写法。
func (o testOutcomes) lookup(spec string) (string, bool) {
	if strings.Contains(spec, "::") {
		status, ok := o.results[spec]
		return status, ok
	}
	status := ""
	found := false
	for key, value := range o.results {
		if !strings.HasSuffix(key, "::"+spec) {
			continue
		}
		if found && status != value {
			return "", false // 同名测试状态冲突，视为无法判定
		}
		status, found = value, true
	}
	return status, found
}

// summarizeTests 按期望测试名列表给出通过情况。
func summarizeTests(specs []string, outcomes testOutcomes) report.TestSummary {
	summary := report.TestSummary{Total: len(specs)}
	for _, spec := range specs {
		status, ok := outcomes.lookup(spec)
		if ok && status == "pass" {
			summary.Passed++
			continue
		}
		summary.Failed = append(summary.Failed, spec)
	}
	return summary
}
