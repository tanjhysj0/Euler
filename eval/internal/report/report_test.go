package report

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestPredictionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.jsonl")
	want := Prediction{
		InstanceID: "demo-001", ModelNameOrPath: "euler-local",
		ModelPatch: "diff --git a/a b/a\n",
		Meta:       Meta{Status: "completed", ToolCalls: 3},
	}
	if err := WritePredictions(path, []Prediction{want}); err != nil {
		t.Fatalf("WritePredictions: %v", err)
	}
	got, err := ReadPredictions(path)
	if err != nil {
		t.Fatalf("ReadPredictions: %v", err)
	}
	if len(got) != 1 || got[0].InstanceID != want.InstanceID || got[0].Meta.ToolCalls != 3 {
		t.Fatalf("got = %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	results := []TaskResult{
		{InstanceID: "a", Resolved: true},
		{InstanceID: "b", Resolved: false, Reason: "patch_failed"},
		{InstanceID: "c", Resolved: false, Reason: "build_failed"},
	}
	preds := []Prediction{
		{InstanceID: "d", Meta: Meta{Status: "error"}},
		{InstanceID: "e", Meta: Meta{Status: "completed"}},
	}
	totals := Summarize(results, preds)
	if totals.Tasks != 5 || totals.Resolved != 1 || totals.PatchFailed != 1 ||
		totals.BuildFailed != 1 || totals.Error != 1 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestPrintSummaryContainsRows(t *testing.T) {
	var buf bytes.Buffer
	report := Report{
		Totals: Totals{Tasks: 1, Resolved: 1, ResolvedRate: 1},
		Tasks: []TaskResult{{
			InstanceID: "demo-001", Resolved: true,
			FailToPass: TestSummary{Total: 2, Passed: 2},
			PassToPass: TestSummary{Total: 2, Passed: 2},
		}},
	}
	PrintSummary(&buf, report)
	out := buf.String()
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "demo-001") {
		t.Fatalf("输出 = %q", out)
	}
}
