package modelprofile

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// Repository persists model profiles. It never sees or stores API keys.
type Repository interface {
	Create(ctx context.Context, profile persistence.ModelProfile) error
	List(ctx context.Context) ([]persistence.ModelProfile, error)
	Get(ctx context.Context, id uuid.UUID) (persistence.ModelProfile, error)
}

// GormRepository stores model profiles in PostgreSQL.
type GormRepository struct {
	db *gorm.DB
}

// NewGormRepository adapts db to Repository.
func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

// Create inserts a model profile row.
func (r *GormRepository) Create(ctx context.Context, profile persistence.ModelProfile) error {
	return r.db.WithContext(ctx).Create(&profile).Error
}

// List returns all model profiles ordered by creation time.
func (r *GormRepository) List(ctx context.Context) ([]persistence.ModelProfile, error) {
	var profiles []persistence.ModelProfile
	err := r.db.WithContext(ctx).Order("created_at").Find(&profiles).Error
	return profiles, err
}

// Get loads a single model profile by ID.
func (r *GormRepository) Get(ctx context.Context, id uuid.UUID) (persistence.ModelProfile, error) {
	var profile persistence.ModelProfile
	err := r.db.WithContext(ctx).First(&profile, "id = ?", id).Error
	return profile, err
}
