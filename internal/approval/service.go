// Package approval 将敏感工具的执行置于明确的人工决策之后：
// 运行会停在 waiting_approval 状态，直到决策被记录。
package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"euler/internal/event"
	"euler/internal/run"
)

const (
	// StatusPending 表示审批正在等待决策。
	StatusPending = "pending"
	// StatusApproved 表示允许工具调用继续执行的决策。
	StatusApproved = "approved"
	// StatusRejected 表示拒绝工具调用的决策。
	StatusRejected = "rejected"
	// StatusCancelled 表示因所属运行停止等待而被关闭的审批。
	StatusCancelled = "cancelled"

	// EventRequested 是审批创建事件的名称。
	EventRequested = "approval.requested"
	// EventResolved 是审批决策事件的名称。
	EventResolved = "approval.resolved"

	// keyApprovalID 是事件载荷中的审批 id 字段。
	keyApprovalID = "approval_id"
	// keyToolCallID 是事件载荷中的工具调用 id 字段。
	keyToolCallID = "tool_call_id"
	// keyName 是事件载荷中的工具名称字段。
	keyName = "name"
	// keyRequest 是请求事件中待审核工具参数的字段。
	keyRequest = "request"
	// keyStatus 是决策事件中的审批状态字段。
	keyStatus = "status"
	// keyApproved 是事件载荷中的决策结果字段。
	keyApproved = "approved"
	// keyReason 是事件载荷中的决策理由字段。
	keyReason = "reason"

	// decisionBuffer 让决策在 Require 仍在发布事件时也能送达。
	decisionBuffer = 1
)

var (
	// ErrAlreadyDecided 拒绝对同一审批的第二次决策。
	ErrAlreadyDecided = errors.New("approval already decided")
	// ErrNotFound 表示未知的审批 id。
	ErrNotFound = errors.New("approval not found")
	// ErrInvalidRequest 表示缺少运行或工具调用的审批请求。
	ErrInvalidRequest = errors.New("invalid approval request")
)

// Gate 阻塞工具调用，直到人工决策被记录。Service 是生产实现；
// agent 循环中的工具注册只依赖此接口。
type Gate interface {
	Require(ctx context.Context, request Request) (Decision, error)
}

// Decision 是一个等待者针对一个审批观察到的决策结果。
type Decision struct {
	Approved bool   `json:"approved"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Request 描述需要审批的工具调用。ID 由 Service 分配，调用方保持零值；
// 持久化行与等待者注册表共用该 ID。
type Request struct {
	ID         uuid.UUID      `json:"id"`
	RunID      uuid.UUID      `json:"run_id"`
	ToolCallID uuid.UUID      `json:"tool_call_id"`
	Name       string         `json:"name"`
	Payload    map[string]any `json:"payload"`
}

// Resolution 是写入存储的一次决策。
type Resolution struct {
	Status   string
	Approved bool
	Reason   string
}

// Record 是一个审批的持久化视图。
type Record struct {
	ID          uuid.UUID  `json:"id"`
	RunID       uuid.UUID  `json:"run_id"`
	ToolCallID  uuid.UUID  `json:"tool_call_id"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	Decision    Decision   `json:"decision"`
}

// Store 持久化审批及其所辖的运行与工具调用状态。实现在同一个数据库
// 事务中写入状态和对应事件并返回该事件，调用方只在提交后才发布它。
type Store interface {
	// Open 写入待决策的审批，并把运行停在 waiting_approval 状态。
	Open(ctx context.Context, request Request) (event.Event, error)
	// Resolve 记录 id 的首次决策，否则返回 ErrAlreadyDecided。
	Resolve(ctx context.Context, id uuid.UUID, resolution Resolution) (Record, event.Event, error)
	// Get 加载一个审批。
	Get(ctx context.Context, id uuid.UUID) (Record, error)
}

// Broadcaster 将已提交的事件投递给在线订阅者。*event.Store 实现了它；
// 事件追加保留在 Store 中，以保证顺序的事务性。
type Broadcaster interface {
	Publish(committed event.Event)
}

// Service 基于持久化 Store 和内存等待者表实现 Gate。
type Service struct {
	store   Store
	events  Broadcaster
	mu      sync.Mutex
	waiters map[uuid.UUID]chan Decision
}

// NewService 组装审批服务。
func NewService(store Store, events Broadcaster) *Service {
	return &Service{
		store:   store,
		events:  events,
		waiters: make(map[uuid.UUID]chan Decision),
	}
}

var _ Gate = (*Service)(nil)

// Require 持久化一个待决策的审批，发布 approval.requested 并阻塞，
// 直到决策到达。context 被取消时会取消该审批及其工具调用并返回
// context 错误；若某个决策在竞争中胜出，则改为返回该决策。
func (s *Service) Require(ctx context.Context, request Request) (Decision, error) {
	if err := validate(request); err != nil {
		return Decision{}, err
	}
	request.ID = uuid.New()
	waiter := s.watch(request.ID)
	created, err := s.store.Open(ctx, request)
	if err != nil {
		s.stop(request.ID)
		return Decision{}, err
	}
	s.events.Publish(created)
	return s.await(ctx, request.ID, waiter)
}

// Decide 记录 id 的决策。首次调用生效：状态、事件和投递按此顺序发生，
// 第二次调用返回 ErrAlreadyDecided，且不触碰数据库。
func (s *Service) Decide(
	ctx context.Context,
	id uuid.UUID,
	resolution Resolution,
) (Record, error) {
	record, resolved, err := s.store.Resolve(ctx, id, resolution)
	if err != nil {
		return Record{}, err
	}
	s.events.Publish(resolved)
	s.deliver(record.ID, record.Decision)
	return record, nil
}

// Get 加载一个审批，供状态轮询和测试使用。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Record, error) {
	return s.store.Get(ctx, id)
}

// await 在决策 channel 和调用方 context 上阻塞等待。
func (s *Service) await(
	ctx context.Context,
	id uuid.UUID,
	waiter chan Decision,
) (Decision, error) {
	select {
	case decision := <-waiter:
		s.stop(id)
		return decision, nil
	case <-ctx.Done():
		return s.abandon(ctx, id)
	}
}

// abandon 关闭已无人等待的审批。它在移除 channel 的同时再次检查，
// 使同一瞬间记录的决策被返回，而不会被取消覆盖。
func (s *Service) abandon(ctx context.Context, id uuid.UUID) (Decision, error) {
	decision, settled := s.reclaim(id)
	if settled {
		return decision, nil
	}
	resolution := Resolution{Status: StatusCancelled}
	record, resolved, err := s.store.Resolve(
		context.WithoutCancel(ctx), id, resolution)
	switch {
	case errors.Is(err, ErrAlreadyDecided):
		// 决策路径已经发布了 approval.resolved。
		return record.Decision, nil
	case err != nil:
		return Decision{}, errors.Join(ctx.Err(), err)
	}
	s.events.Publish(resolved)
	return Decision{Status: StatusCancelled}, ctx.Err()
}

func validate(request Request) error {
	switch {
	case request.RunID == uuid.Nil, request.ToolCallID == uuid.Nil:
		return ErrInvalidRequest
	case request.Name == "":
		return ErrInvalidRequest
	default:
		return nil
	}
}

// watch 在任何数据落库之前为 id 注册等待者 channel，
// 因此决策绝不会投递到尚不存在的表项。
func (s *Service) watch(id uuid.UUID) chan Decision {
	waiter := make(chan Decision, decisionBuffer)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiters[id] = waiter
	return waiter
}

// stop 注销 id 的等待者 channel。
func (s *Service) stop(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiters, id)
}

// reclaim 注销 id，并报告抢先到达的决策。
func (s *Service) reclaim(id uuid.UUID) (Decision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter, ok := s.waiters[id]
	if !ok {
		return Decision{}, false
	}
	delete(s.waiters, id)
	select {
	case decision := <-waiter:
		return decision, true
	default:
		return Decision{}, false
	}
}

// deliver 将持久化的决策交给已注册的等待者（若有）。一个审批 id
// 至多被决策一次，因此带缓冲的发送永远不会阻塞。
func (s *Service) deliver(id uuid.UUID, decision Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter, ok := s.waiters[id]
	if !ok {
		return
	}
	select {
	case waiter <- decision:
	default:
		return
	}
}

// requestedPayload 构造 approval.requested 的事件体。
func requestedPayload(request Request) map[string]any {
	payload := request.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{
		keyApprovalID: request.ID.String(),
		keyToolCallID: request.ToolCallID.String(),
		keyName:       request.Name,
		keyRequest:    payload,
	}
}

// resolvedPayload 构造 approval.resolved 的事件体。
func resolvedPayload(
	id uuid.UUID,
	toolCallID uuid.UUID,
	resolution Resolution,
) map[string]any {
	return map[string]any{
		keyApprovalID: id.String(),
		keyToolCallID: toolCallID.String(),
		keyStatus:     resolution.Status,
		keyApproved:   resolution.Approved,
		keyReason:     resolution.Reason,
	}
}

// runStatusChange 在审批前后停驻或释放一个运行。
func runStatusChange(id uuid.UUID, from, to string) run.StatusChange {
	return run.StatusChange{RunID: id, From: from, To: to}
}
