package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"

	"euler/internal/run"
)

func TestStaticLoaderRejectsMissingWorkspace(t *testing.T) {
	_, err := (StaticLoader{}).Load(context.Background(), run.Request{Input: "hi"})
	if !errors.Is(err, ErrMissingWorkspace) {
		t.Fatalf("want ErrMissingWorkspace, got %v", err)
	}
}

func TestStaticLoaderRejectsEmptyInstruction(t *testing.T) {
	_, err := (StaticLoader{Dir: "/app"}).Load(context.Background(), run.Request{})
	if !errors.Is(err, ErrMissingInstruction) {
		t.Fatalf("want ErrMissingInstruction, got %v", err)
	}
}

func TestStaticLoaderBuildsSingleTurnHistory(t *testing.T) {
	loader := StaticLoader{Dir: "/app", Model: ModelSpec{Model: "m"}}
	input, err := loader.Load(context.Background(), run.Request{Input: "fix"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if input.Dir != "/app" || input.Model.Model != "m" {
		t.Fatalf("unexpected input: %+v", input)
	}
	if len(input.History) != 2 {
		t.Fatalf("want 2 messages, got %d", len(input.History))
	}
	if input.History[0].Role != schema.System || input.History[1].Role != schema.User {
		t.Fatalf("unexpected roles: %v, %v", input.History[0].Role, input.History[1].Role)
	}
	if input.History[1].Content != "fix" {
		t.Fatalf("unexpected user content: %q", input.History[1].Content)
	}
}
