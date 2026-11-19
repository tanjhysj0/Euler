package approval

import (
	"context"
	"sync"
)

// AutoApprove 无条件批准所有审批请求，供评测与自动化流程使用。
// 它不产生持久化记录，也不发布事件。
type AutoApprove struct{}

// Require 校验请求后直接返回已批准的裁决。
func (AutoApprove) Require(_ context.Context, request Request) (Decision, error) {
	if err := validate(request); err != nil {
		return Decision{}, err
	}
	return Decision{Approved: true, Status: StatusApproved}, nil
}

// DenyAll 拒绝所有审批请求，用于安全测试与只读场景。
// 它不产生持久化记录，也不发布事件。
type DenyAll struct{}

// Require 校验请求后直接返回已拒绝的裁决。
func (DenyAll) Require(_ context.Context, request Request) (Decision, error) {
	if err := validate(request); err != nil {
		return Decision{}, err
	}
	return Decision{Approved: false, Status: StatusRejected}, nil
}

// Scripted 按工具名放行审批请求，未列出的工具一律拒绝。
// 它记录收到的全部请求，便于测试断言调用顺序与参数。
type Scripted struct {
	mu       sync.Mutex
	approved map[string]bool
	requests []Request
}

// NewScripted 用工具名到放行结果的映射构建脚本化审批。
func NewScripted(approved map[string]bool) *Scripted {
	allowed := make(map[string]bool, len(approved))
	for name, ok := range approved {
		allowed[name] = ok
	}
	return &Scripted{approved: allowed}
}

// Require 校验并记录请求，再按工具名返回裁决。
func (s *Scripted) Require(_ context.Context, request Request) (Decision, error) {
	if err := validate(request); err != nil {
		return Decision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if !s.approved[request.Name] {
		return Decision{Approved: false, Status: StatusRejected}, nil
	}
	return Decision{Approved: true, Status: StatusApproved}, nil
}

// Requests 返回按到达顺序记录的请求副本。
func (s *Scripted) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]Request, len(s.requests))
	copy(requests, s.requests)
	return requests
}
