package judge

import (
	"strings"
	"testing"
)

func TestParseTestOutput(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"run","Package":"example/a","Test":"TestA"}`,
		`{"Action":"pass","Package":"example/a","Test":"TestA"}`,
		`{"Action":"run","Package":"example/b","Test":"TestB"}`,
		`{"Action":"fail","Package":"example/b","Test":"TestB"}`,
		`not json line`,
	}, "\n")
	outcomes := parseTestOutput(strings.NewReader(input))
	if status, ok := outcomes.lookup("TestA"); !ok || status != "pass" {
		t.Fatalf("TestA = %q, %v", status, ok)
	}
	if status, ok := outcomes.lookup("example/b::TestB"); !ok || status != "fail" {
		t.Fatalf("TestB = %q, %v", status, ok)
	}
	if _, ok := outcomes.lookup("TestMissing"); ok {
		t.Fatal("不存在的测试不应被找到")
	}
}

func TestSummarizeTests(t *testing.T) {
	outcomes := parseTestOutput(strings.NewReader(strings.Join([]string{
		`{"Action":"pass","Package":"p","Test":"TestA"}`,
		`{"Action":"fail","Package":"p","Test":"TestB"}`,
	}, "\n")))
	summary := summarizeTests([]string{"TestA", "TestB", "TestC"}, outcomes)
	if summary.Total != 3 || summary.Passed != 1 || len(summary.Failed) != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}
