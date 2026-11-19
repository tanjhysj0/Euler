// Package session 负责会话生命周期、工作区绑定和消息历史。
package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"euler/internal/persistence"
	"euler/internal/workspace"
)

const (
	statusActive        = "active"
	defaultMessageLimit = 100
	maxMessageLimit     = 200
	defaultSessionTitle = "新会话"
	emptyString         = ""
	emptyLength         = 0
	seqCursorOffset     = 1
	idFilter            = "id = ?"
)

var (
	// ErrInvalidInput 表示无法接受的会话输入。
	ErrInvalidInput = errors.New("invalid session input")
	// ErrNotFound 表示会话不存在。
	ErrNotFound = errors.New("session not found")

	activeRunStatuses = []string{"queued", "running", "waiting_approval"}
)

// CreateInput 承载 Create 接受的字段。Path 为空时，
// 服务会创建一个默认的工作区目录。
type CreateInput struct {
	ProfileID uuid.UUID
	Path      string
}

// ListFilter 用于收窄会话列表的查询范围。
type ListFilter struct {
	WorkspacePath string
	Query         string
	ActiveRun     bool
}

// Service 管理会话及其消息历史。
type Service struct {
	db         *gorm.DB
	workspaces *workspace.Service
}

// NewService 基于 db 和工作区目录辅助服务构建 Service。
func NewService(db *gorm.DB, workspaces *workspace.Service) *Service {
	return &Service{db: db, workspaces: workspaces}
}

// Create 准备工作区目录、存储会话，并报告同一路径上的其他会话
// 是否有活跃的 run。
func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	sessionID := uuid.New()
	path, created, err := s.preparePath(sessionID, input.Path)
	if err != nil {
		return CreateResult{}, err
	}
	result, err := s.insert(ctx, sessionID, input.ProfileID, path)
	if err == nil {
		return result, nil
	}
	if cleanupErr := s.workspaces.CleanupCreated(path, created); cleanupErr != nil {
		return CreateResult{}, errors.Join(err, cleanupErr)
	}
	return CreateResult{}, err
}

// Get 返回一个会话及其工作区和模型摘要。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (SessionView, error) {
	var session persistence.Session
	err := s.db.WithContext(ctx).First(&session, idFilter, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SessionView{}, ErrNotFound
	}
	if err != nil {
		return SessionView{}, fmt.Errorf("load session: %w", err)
	}
	var workspaceRow persistence.Workspace
	err = s.db.WithContext(ctx).First(&workspaceRow, idFilter, session.WorkspaceID).Error
	if err != nil {
		return SessionView{}, fmt.Errorf("load workspace: %w", err)
	}
	var profile persistence.ModelProfile
	err = s.db.WithContext(ctx).First(&profile, idFilter, session.ModelProfileID).Error
	if err != nil {
		return SessionView{}, fmt.Errorf("load model profile: %w", err)
	}
	return toSessionView(session, workspaceRow, profile), nil
}

// List 返回按最近活跃时间排序的会话。
func (s *Service) List(ctx context.Context, filter ListFilter) ([]SessionView, error) {
	query, err := s.listQuery(ctx, filter)
	if err != nil {
		return nil, err
	}
	var rows []sessionRow
	if err = query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	views := make([]SessionView, len(rows))
	for i, row := range rows {
		views[i] = row.toView()
	}
	return views, nil
}

// Messages 返回按会话序号排序的一页消息。
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
	for _, row := range rows {
		page.Messages = append(page.Messages, toMessageView(row))
	}
	return page, nil
}

func (s *Service) preparePath(sessionID uuid.UUID, path string) (string, bool, error) {
	if path == emptyString {
		createdPath, created, err := s.workspaces.CreateDefault(sessionID)
		if err != nil {
			return emptyString, false, err
		}
		return createdPath, created, nil
	}
	normalized, err := s.workspaces.NormalizeExisting(path)
	if err != nil {
		return emptyString, false, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return normalized, false, nil
}

func (s *Service) insert(
	ctx context.Context, sessionID, profileID uuid.UUID, path string,
) (CreateResult, error) {
	var result CreateResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		profile, err := loadProfile(tx, profileID)
		if err != nil {
			return err
		}
		if err = lockWorkspacePath(tx, path); err != nil {
			return err
		}
		workspaceRow, err := lockOrCreateWorkspace(tx, path)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		session := persistence.Session{
			ID:             sessionID,
			WorkspaceID:    workspaceRow.ID,
			ModelProfileID: profileID,
			Title:          defaultTitle(path),
			Status:         statusActive,
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActiveAt:   now,
		}
		if err = tx.Create(&session).Error; err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		warning, err := hasOtherActiveRun(tx, workspaceRow.ID, sessionID)
		if err != nil {
			return err
		}
		result = CreateResult{
			SessionView:      toSessionView(session, workspaceRow, profile),
			WorkspaceWarning: warning,
		}
		return nil
	})
	if err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

func (s *Service) listQuery(ctx context.Context, filter ListFilter) (*gorm.DB, error) {
	query := s.db.WithContext(ctx).
		Table("sessions").
		Select(sessionColumns).
		Joins("JOIN workspaces ON workspaces.id = sessions.workspace_id").
		Joins("JOIN model_profiles ON model_profiles.id = sessions.model_profile_id").
		Order("sessions.last_active_at DESC")
	if filter.Query != emptyString {
		query = query.Where("sessions.title ILIKE ?", "%"+filter.Query+"%")
	}
	path, err := s.filterPath(filter.WorkspacePath)
	if err != nil {
		return nil, err
	}
	if path != emptyString {
		query = query.Where("workspaces.path = ?", path)
	}
	if filter.ActiveRun {
		query = query.Where(
			"EXISTS (SELECT 1 FROM runs WHERE runs.session_id = sessions.id "+
				"AND runs.status IN ?)", activeRunStatuses,
		)
	}
	return query, nil
}

func (s *Service) filterPath(path string) (string, error) {
	if path == emptyString {
		return emptyString, nil
	}
	normalized, err := s.workspaces.NormalizeExisting(path)
	if err != nil {
		return emptyString, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return normalized, nil
}

func (s *Service) ensureSession(ctx context.Context, id uuid.UUID) error {
	var count int64
	err := s.db.WithContext(ctx).Model(&persistence.Session{}).
		Where(idFilter, id).Count(&count).Error
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if count == emptyLength {
		return ErrNotFound
	}
	return nil
}

const sessionColumns = `sessions.id, sessions.title, sessions.status,
	sessions.created_at, sessions.updated_at, sessions.last_active_at,
	workspaces.id AS workspace_id, workspaces.path AS workspace_path,
	model_profiles.id AS profile_id, model_profiles.name AS profile_name,
	model_profiles.base_url AS profile_base_url, model_profiles.model AS profile_model`

type sessionRow struct {
	ID             uuid.UUID
	Title          string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActiveAt   time.Time
	WorkspaceID    uuid.UUID
	WorkspacePath  string
	ProfileID      uuid.UUID
	ProfileName    string
	ProfileBaseURL string
	ProfileModel   string
}

func (r sessionRow) toView() SessionView {
	return SessionView{
		ID:     r.ID,
		Title:  r.Title,
		Status: r.Status,
		Workspace: WorkspaceView{
			ID:   r.WorkspaceID,
			Path: r.WorkspacePath,
		},
		Model: ModelView{
			ID:      r.ProfileID,
			Name:    r.ProfileName,
			BaseURL: r.ProfileBaseURL,
			Model:   r.ProfileModel,
		},
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		LastActiveAt: r.LastActiveAt,
	}
}

func loadProfile(tx *gorm.DB, id uuid.UUID) (persistence.ModelProfile, error) {
	var profile persistence.ModelProfile
	err := tx.First(&profile, idFilter, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return persistence.ModelProfile{},
			fmt.Errorf("%w: model profile not found", ErrInvalidInput)
	}
	if err != nil {
		return persistence.ModelProfile{}, fmt.Errorf("load model profile: %w", err)
	}
	return profile, nil
}

func lockWorkspacePath(tx *gorm.DB, path string) error {
	// 不同路径的哈希键可能碰撞，这只会让不相关的创建操作被串行化，
	// 绝不会削弱正确性。
	err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", path).Error
	if err != nil {
		return fmt.Errorf("lock workspace path: %w", err)
	}
	return nil
}

func lockOrCreateWorkspace(tx *gorm.DB, path string) (persistence.Workspace, error) {
	var row persistence.Workspace
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("path = ?", path).
		First(&row).Error
	switch {
	case err == nil:
		return row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = persistence.Workspace{ID: uuid.New(), Path: path}
		if createErr := tx.Create(&row).Error; createErr != nil {
			return persistence.Workspace{}, fmt.Errorf("create workspace: %w", createErr)
		}
		return row, nil
	default:
		return persistence.Workspace{}, fmt.Errorf("lock workspace: %w", err)
	}
}

func hasOtherActiveRun(tx *gorm.DB, workspaceID, sessionID uuid.UUID) (bool, error) {
	var count int64
	err := tx.Table("runs").
		Joins("JOIN sessions ON sessions.id = runs.session_id").
		Where("sessions.workspace_id = ?", workspaceID).
		Where("sessions.id <> ?", sessionID).
		Where("runs.status IN ?", activeRunStatuses).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check active runs: %w", err)
	}
	return count > emptyLength, nil
}

func toSessionView(
	session persistence.Session,
	workspaceRow persistence.Workspace,
	profile persistence.ModelProfile,
) SessionView {
	return sessionRow{
		ID:             session.ID,
		Title:          session.Title,
		Status:         session.Status,
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
		LastActiveAt:   session.LastActiveAt,
		WorkspaceID:    workspaceRow.ID,
		WorkspacePath:  workspaceRow.Path,
		ProfileID:      profile.ID,
		ProfileName:    profile.Name,
		ProfileBaseURL: profile.BaseURL,
		ProfileModel:   profile.Model,
	}.toView()
}

func toMessageView(row persistence.Message) MessageView {
	return MessageView{
		ID:          row.ID,
		RunID:       row.RunID,
		SessionSeq:  row.SessionSeq,
		Role:        row.Role,
		Content:     row.Content,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		CompletedAt: row.CompletedAt,
	}
}

func defaultTitle(path string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == ".." {
		return defaultSessionTitle
	}
	return base
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
