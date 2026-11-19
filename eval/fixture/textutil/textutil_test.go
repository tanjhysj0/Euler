package textutil

import (
	"reflect"
	"testing"
)

func TestSplitBasic(t *testing.T) {
	got := Split("a,b,c", ",")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %v, want %v", got, want)
	}
}

func TestSplitEmptyInput(t *testing.T) {
	if got := Split("", ","); got != nil {
		t.Fatalf("Split(空) = %v, 期望 nil", got)
	}
}

func TestJoin(t *testing.T) {
	if got := Join([]string{"a", "b"}, "-"); got != "a-b" {
		t.Fatalf("Join = %q", got)
	}
}
