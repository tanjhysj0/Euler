package calculator

import (
	"errors"
	"testing"
)

func TestEvalPrecedence(t *testing.T) {
	got, err := Eval("1+2*3")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got != 7 {
		t.Fatalf("Eval = %d, want 7", got)
	}
}

func TestEvalParens(t *testing.T) {
	got, err := Eval("(1+2)*3")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got != 9 {
		t.Fatalf("Eval = %d, want 9", got)
	}
}

func TestEvalDivideByZero(t *testing.T) {
	if _, err := Eval("1/0"); !errors.Is(err, ErrDivideByZero) {
		t.Fatalf("err = %v, want ErrDivideByZero", err)
	}
}

func TestEvalInvalid(t *testing.T) {
	if _, err := Eval("1+"); !errors.Is(err, ErrInvalidExpression) {
		t.Fatalf("err = %v, want ErrInvalidExpression", err)
	}
}
