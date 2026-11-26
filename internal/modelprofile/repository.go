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
	Update(ctx context.Context, profile persistence.ModelProfile) error
	// Delete 删除配置，并把引用它的会话改绑到最早创建的剩余配置，
	// 返回改绑的会话数。仅剩一个配置时返回 ErrLastProfile。
	Delete(ctx context.Context, id uuid.UUID) (int64, error)
	List(ctx context.Context) ([]persistence.ModelProfile, error)
	Get(ctx context.Context, id uuid.UUID) (persistence.ModelProfile, error)
}

// GormRepository 将模型配置存储在 SQLite 中。
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

// Update 保存已有的模型配置记录。
func (r *GormRepository) Update(ctx context.Context, profile persistence.ModelProfile) error {
	return r.db.WithContext(ctx).Save(&profile).Error
}

// Delete 在一个事务里改绑引用会话并删除配置，避免会话外键阻止删除。
func (r *GormRepository) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	var rebound int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var profile persistence.ModelProfile
		if err := tx.First(&profile, "id = ?", id).Error; err != nil {
			return err
		}
		var total int64
		if err := tx.Model(&persistence.ModelProfile{}).Count(&total).Error; err != nil {
			return err
		}
		if total <= 1 {
			return ErrLastProfile
		}
		var replacement persistence.ModelProfile
		err := tx.Where("id <> ?", id).Order("created_at ASC").First(&replacement).Error
		if err != nil {
			return err
		}
		result := tx.Model(&persistence.Session{}).
			Where("model_profile_id = ?", id).
			Update("model_profile_id", replacement.ID)
		if result.Error != nil {
			return result.Error
		}
		rebound = result.RowsAffected
		return tx.Delete(&persistence.ModelProfile{}, "id = ?", id).Error
	})
	return rebound, err
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
