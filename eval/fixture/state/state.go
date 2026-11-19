// Package state 实现订单状态机。
package state

import "errors"

// Status 是订单状态。
type Status string

const (
	// StatusCreated 表示订单已创建。
	StatusCreated Status = "created"
	// StatusPaid 表示订单已支付。
	StatusPaid Status = "paid"
	// StatusShipped 表示订单已发货。
	StatusShipped Status = "shipped"
	// StatusCancelled 表示订单已取消。
	StatusCancelled Status = "cancelled"
)

// ErrInvalidTransition 表示非法的状态迁移。
var ErrInvalidTransition = errors.New("invalid transition")

// Order 是订单聚合。
type Order struct{ status Status }

// New 创建处于 created 状态的订单。
func New() *Order { return &Order{status: StatusCreated} }

// Status 返回当前状态。
func (o *Order) Status() Status { return o.status }

// Pay 将订单置为已支付，仅允许从 created 迁移。
func (o *Order) Pay() error {
	if o.status != StatusCreated {
		return ErrInvalidTransition
	}
	o.status = StatusPaid
	return nil
}

// Ship 将订单置为已发货，仅允许从 paid 迁移。
func (o *Order) Ship() error {
	if o.status != StatusPaid {
		return ErrInvalidTransition
	}
	o.status = StatusShipped
	return nil
}

// Cancel 取消订单，已发货或已取消时拒绝。
func (o *Order) Cancel() error {
	if o.status == StatusShipped || o.status == StatusCancelled {
		return ErrInvalidTransition
	}
	o.status = StatusCancelled
	return nil
}
