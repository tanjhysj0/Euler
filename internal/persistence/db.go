// Package persistence owns the PostgreSQL schema, entities and connection setup.
package persistence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the postgres driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // registers the file source
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	maxOpenConns    = 20
	maxIdleConns    = 5
	connMaxLifetime = 30 * time.Minute
)

// Open connects GORM to PostgreSQL, configures the pool and verifies liveness.
func Open(ctx context.Context, databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)
	if err = sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// Migrate applies all pending migrations found in the migrations directory.
func Migrate(databaseURL string) error {
	dir, err := migrationsDir()
	if err != nil {
		return fmt.Errorf("locate migrations: %w", err)
	}
	migrator, err := migrate.New("file://"+dir, databaseURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		if closeErr := errors.Join(sourceErr, databaseErr); closeErr != nil {
			slog.Warn("close migrator", "error", closeErr)
		}
	}()
	if err = migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// migrationsDir locates the migrations directory: the working directory
// first (repository checkout and tests), then next to or above the running
// executable so an installed binary also finds its migrations.
func migrationsDir() (string, error) {
	candidates := []string{"migrations"}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "migrations"),
			filepath.Join(exeDir, "..", "migrations"))
	}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
			return abs, nil
		}
	}
	return "", errors.New("migrations directory not found")
}

// HealthChecker adapts a GORM connection to the API health endpoint.
type HealthChecker struct {
	DB *gorm.DB
}

// Ping reports whether the underlying connection pool answers a ping.
func (h HealthChecker) Ping(ctx context.Context) error {
	sqlDB, err := h.DB.DB()
	if err != nil {
		return fmt.Errorf("get sql db: %w", err)
	}
	if err = sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
