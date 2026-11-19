package state

import "testing"

func TestPayThenShip(t *testing.T) {
	order := New()
	if err := order.Pay(); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if err := order.Ship(); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if order.Status() != StatusShipped {
		t.Fatalf("Status = %s", order.Status())
	}
}

func TestCancelAfterPaid(t *testing.T) {
	order := New()
	if err := order.Pay(); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if order.Status() != StatusCancelled {
		t.Fatalf("Status = %s", order.Status())
	}
}

func TestCancelFromCreated(t *testing.T) {
	order := New()
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
