package modelprofile

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// Repository 持久化模型配置。它从不接触或存储 API key。
type Repository interface {
	Create(ctx context.Context, profile persistence.ModelProfile) error
	List(ctx context.Context) ([]persistence.ModelProfile, error)
	Get(ctx context.Context, id uuid.UUID) (persistence.ModelProfile, error)
}

// GormRepository 将模型配置存储在 PostgreSQL 中。
type GormRepository struct {
	db *gorm.DB
}

// NewGormRepository 将 db 适配为 Repository。
func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

// Create 插入一条模型配置记录。
func (r *GormRepository) Create(ctx context.Context, profile persistence.ModelProfile) error {
	return r.db.WithContext(ctx).Create(&profile).Error
}

// List 返回按创建时间排序的所有模型配置。
func (r *GormRepository) List(ctx context.Context) ([]persistence.ModelProfile, error) {
	var profiles []persistence.ModelProfile
	err := r.db.WithContext(ctx).Order("created_at").Find(&profiles).Error
	return profiles, err
}

// Get 按 ID 加载单条模型配置。
func (r *GormRepository) Get(ctx context.Context, id uuid.UUID) (persistence.ModelProfile, error) {
	var profile persistence.ModelProfile
	err := r.db.WithContext(ctx).First(&profile, "id = ?", id).Error
	return profile, err
}
