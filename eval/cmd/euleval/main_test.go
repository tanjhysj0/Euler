package main

import "testing"

func TestSplitIDs(t *testing.T) {
	got := splitIDs(" a, b ,,c ")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("splitIDs = %v", got)
	}
	if splitIDs("  ") != nil {
		t.Fatal("空白输入应返回 nil")
	}
}
