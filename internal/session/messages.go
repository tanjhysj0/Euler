package session

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"euler/internal/persistence"
)

const (
	roleAssistant       = "assistant"
	defaultMessageLimit = 100
	maxMessageLimit     = 200
	seqCursorOffset     = 1
)

// Messages 返回按会话序号排序的一页消息，并为助手消息补齐工具详情。
func (s *Service) Messages(
	ctx context.Context, id uuid.UUID, after int64, limit int,
) (MessagePage, error) {
	if err := s.ensureSession(ctx, id); err != nil {
		return MessagePage{}, err
	}
	if after < emptyLength {
		after = emptyLength
	}
	limit = normalizeLimit(limit)
	var rows []persistence.Message
	err := s.db.WithContext(ctx).
		Where("session_id = ? AND session_seq > ?", id, after).
		Order("session_seq ASC").
		Limit(limit + 1).
		Find(&rows).Error
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", err)
	}
	page := MessagePage{Messages: make([]MessageView, emptyLength, len(rows))}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextAfter = rows[limit-seqCursorOffset].SessionSeq
	}
	tools, err := s.toolCallsByRun(ctx, assistantRunIDs(rows))
	if err != nil {
		return MessagePage{}, err
	}
	for _, row := range rows {
		page.Messages = append(page.Messages, toMessageView(row, tools))
	}
	return page, nil
}

// assistantRunIDs 收集本页助手消息涉及的 Run，用于一次查询补齐工具详情。
func assistantRunIDs(rows []persistence.Message) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(rows))
	ids := make([]uuid.UUID, emptyLength, len(rows))
	for _, row := range rows {
		if row.Role != roleAssistant || row.RunID == nil {
			continue
		}
		if _, ok := seen[*row.RunID]; ok {
			continue
		}
		seen[*row.RunID] = struct{}{}
		ids = append(ids, *row.RunID)
	}
	return ids
}

// toolCallsByRun 按 Run 分组读取工具调用，避免前端逐条消息再拉快照。
func (s *Service) toolCallsByRun(
	ctx context.Context, runIDs []uuid.UUID,
) (map[uuid.UUID][]persistence.ToolCall, error) {
	byRun := make(map[uuid.UUID][]persistence.ToolCall, len(runIDs))
	if len(runIDs) == emptyLength {
		return byRun, nil
	}
	var rows []persistence.ToolCall
	err := s.db.WithContext(ctx).Where("run_id IN ?", runIDs).
		Order("created_at ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list tool calls: %w", err)
	}
	for _, row := range rows {
		byRun[row.RunID] = append(byRun[row.RunID], row)
	}
	return byRun, nil
}

func toMessageView(
	row persistence.Message, tools map[uuid.UUID][]persistence.ToolCall,
) MessageView {
	parts := persistence.ParseParts(row.Parts)
	if len(parts) == emptyLength {
		// 兼容缺失 parts 的旧数据：正文回退为单个文本片段。
		parts = persistence.TextParts(row.Content)
	}
	// 旧数据按事件逐条回填，合并相邻同类片段后按连续段落渲染。
	parts = persistence.CoalesceParts(parts)
	view := MessageView{
		ID:          row.ID,
		RunID:       row.RunID,
		SessionSeq:  row.SessionSeq,
		Role:        row.Role,
		Content:     row.Content,
		Parts:       parts,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		CompletedAt: row.CompletedAt,
	}
	if row.Role == roleAssistant && row.RunID != nil {
		view.ToolCalls = tools[*row.RunID]
	}
	return view
}

func normalizeLimit(limit int) int {
	if limit <= emptyLength {
		return defaultMessageLimit
	}
	if limit > maxMessageLimit {
		return maxMessageLimit
	}
	return limit
}
