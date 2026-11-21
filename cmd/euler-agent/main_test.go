package main

import (
	"context"
	"errors"
	"flag"
	"testing"

	"euler/internal/tools"
)

// TestBindFlagsWritesBackParsedValues 防止 bindFlags 返回结构体副本导致
// flag.Parse 写入的值丢失。
func TestBindFlagsWritesBackParsedValues(t *testing.T) {
	original := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("euler-agent-test", flag.ContinueOnError)
	defer func() { flag.CommandLine = original }()

	opts := bindFlags()
	if err := flag.CommandLine.Parse([]string{
		"-instruction-file", "/tmp/instr.txt", "-model", "m", "-max-rounds", "7",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.instructionFile != "/tmp/instr.txt" {
		t.Fatalf("instruction file not written back: %q", opts.instructionFile)
	}
	if opts.model != "m" || opts.maxRounds != 7 {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestErrorCodePrefersCodedError(t *testing.T) {
	coded := &tools.CodedError{Code: "model_auth", Msg: "unauthorized"}
	if got := errorCode(coded); got != "model_auth" {
		t.Fatalf("want model_auth, got %q", got)
	}
}

func TestErrorCodeMapsDeadlineToRunTimeout(t *testing.T) {
	if got := errorCode(context.DeadlineExceeded); got != codeRunTimeout {
		t.Fatalf("want %s, got %q", codeRunTimeout, got)
	}
}

func TestErrorCodeReturnsEmptyForUnknown(t *testing.T) {
	if got := errorCode(errors.New("boom")); got != emptyInput {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestValidateAllowsUnlimitedRounds(t *testing.T) {
	err := validate(options{instruction: "fix", model: "m", baseURL: "http://x",
		apiKey: "k", maxRounds: 0, maxToolCalls: 1, maxDuration: 1})
	if err != nil {
		t.Fatalf("unlimited rounds should be valid: %v", err)
	}
}

func TestValidateRejectsMissingModel(t *testing.T) {
	err := validate(options{instruction: "fix", baseURL: "http://x", apiKey: "k",
		maxRounds: 1, maxToolCalls: 1, maxDuration: 1})
	if err == nil {
		t.Fatal("want error for missing model")
	}
}
