package store

import "testing"

func TestSetGet(t *testing.T) {
	s := New()
	s.Set("a", 1)
	if value, ok := s.Get("a"); !ok || value != 1 {
		t.Fatalf("Get = %d, %v", value, ok)
	}
}

func TestDeleteMissing(t *testing.T) {
	s := New()
	s.Delete("missing")
	if _, ok := s.Get("missing"); ok {
		t.Fatal("删除不存在的键不应产生值")
	}
}

func TestSum(t *testing.T) {
	s := New()
	s.Set("a", 1)
	s.Set("b", 2)
	if got := s.Sum(); got != 3 {
		t.Fatalf("Sum = %d, want 3", got)
	}
}
