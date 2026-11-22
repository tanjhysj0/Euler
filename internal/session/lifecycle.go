package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// UpdateModel 将会话切换到另一个模型配置；历史 Run 与消息保持不变。
func (s *Service) UpdateModel(
	ctx context.Context, id, profileID uuid.UUID,
) (SessionView, error) {
	if profileID == uuid.Nil {
		return SessionView{}, fmt.Errorf("%w: model profile is required", ErrInvalidInput)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireSession(tx, id); err != nil {
			return err
		}
		if _, err := loadProfile(tx, profileID); err != nil {
			return err
		}
		err := tx.Model(&persistence.Session{}).Where(idFilter, id).
			Update("model_profile_id", profileID).Error
		if err != nil {
			return fmt.Errorf("update session model: %w", err)
		}
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, id)
}

// UpdateWorkspace 将会话切换到另一个已存在的目录；历史 Run 与消息保持不变，
// 后续 Run 与文件面板改用新目录。会话有进行中的 Run 时拒绝切换。
func (s *Service) UpdateWorkspace(
	ctx context.Context, id uuid.UUID, path string,
) (SessionView, error) {
	if path == emptyString {
		return SessionView{}, fmt.Errorf("%w: workspace path is required", ErrInvalidInput)
	}
	normalized, err := s.workspaces.NormalizeExisting(path)
	if err != nil {
		return SessionView{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if txErr := requireSession(tx, id); txErr != nil {
			return txErr
		}
		if txErr := ensureSessionIdle(tx, id); txErr != nil {
			return txErr
		}
		if txErr := lockWorkspacePath(tx, normalized); txErr != nil {
			return txErr
		}
		workspaceRow, txErr := lockOrCreateWorkspace(tx, normalized)
		if txErr != nil {
			return txErr
		}
		update := tx.Model(&persistence.Session{}).Where(idFilter, id).
			Update("workspace_id", workspaceRow.ID)
		if update.Error != nil {
			return fmt.Errorf("update session workspace: %w", update.Error)
		}
		return nil
	})
	if err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, id)
}

// Delete 删除会话及其全部 Run、消息与工具记录。工作区目录与共享工作区
// 记录保留，避免误删用户文件。
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireSession(tx, id); err != nil {
			return err
		}
		if err := ensureSessionIdle(tx, id); err != nil {
			return err
		}
		return deleteSessionData(tx, id)
	})
}

// resolveProfileID 显式传入配置时直接使用，否则回退到默认模型配置。
func resolveProfileID(tx *gorm.DB, profileID uuid.UUID) (uuid.UUID, error) {
	if profileID != uuid.Nil {
		return profileID, nil
	}
	return defaultProfileID(tx)
}

// defaultProfileID 返回最早创建的模型配置作为默认模型。
func defaultProfileID(tx *gorm.DB) (uuid.UUID, error) {
	var profile persistence.ModelProfile
	err := tx.Order("created_at ASC").First(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, ErrNoModelProfile
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load default model profile: %w", err)
	}
	return profile.ID, nil
}

func requireSession(tx *gorm.DB, id uuid.UUID) error {
	var count int64
	err := tx.Model(&persistence.Session{}).Where(idFilter, id).Count(&count).Error
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if count == emptyLength {
		return ErrNotFound
	}
	return nil
}

func ensureSessionIdle(tx *gorm.DB, sessionID uuid.UUID) error {
	var count int64
	err := tx.Model(&persistence.Run{}).
		Where("session_id = ? AND status IN ?", sessionID, activeRunStatuses).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("check session runs: %w", err)
	}
	if count > emptyLength {
		return ErrSessionHasActiveRun
	}
	return nil
}

// deleteSessionData 按外键依赖逆序清理会话数据。工作区记录被多个会话
// 共享，必须保留。
func deleteSessionData(tx *gorm.DB, sessionID uuid.UUID) error {
	// tool_calls.approval_id 与 approvals.tool_call_id 互相引用，直接删 approvals
	// 会触发 tool_calls_approval_id_fkey。先解除 tool_calls 一侧的引用再按序清理。
	if err := tx.Model(&persistence.ToolCall{}).
		Where("run_id IN (?)", tx.Model(&persistence.Run{}).Select("id").
			Where("session_id = ?", sessionID)).
		Update("approval_id", nil).Error; err != nil {
		return fmt.Errorf("clear tool call approvals: %w", err)
	}
	runScoped := []struct {
		model any
		name  string
	}{
		{&persistence.Approval{}, "approvals"},
		{&persistence.ToolCall{}, "tool calls"},
		{&persistence.RunEvent{}, "run events"},
	}
	for _, item := range runScoped {
		subquery := tx.Model(&persistence.Run{}).Select("id").
			Where("session_id = ?", sessionID)
		if err := tx.Where("run_id IN (?)", subquery).Delete(item.model).Error; err != nil {
			return fmt.Errorf("delete %s: %w", item.name, err)
		}
	}
	if err := tx.Where("session_id = ?", sessionID).
		Delete(&persistence.Message{}).Error; err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	if err := tx.Where("session_id = ?", sessionID).
		Delete(&persistence.Run{}).Error; err != nil {
		return fmt.Errorf("delete runs: %w", err)
	}
	if err := tx.Where(idFilter, sessionID).
		Delete(&persistence.Session{}).Error; err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
