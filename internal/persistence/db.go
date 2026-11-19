// Package persistence 负责 PostgreSQL 的 schema、实体和连接建立。
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
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // 注册 postgres 驱动
	_ "github.com/golang-migrate/migrate/v4/source/file"       // 注册 file 源
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	maxOpenConns    = 20
	maxIdleConns    = 5
	connMaxLifetime = 30 * time.Minute
)

// Open 将 GORM 连接到 PostgreSQL，配置连接池并验证连通性。
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

// Migrate 应用 migrations 目录中所有待执行的 migration。
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

// migrationsDir 定位 migrations 目录：先查找工作目录
// （仓库检出和测试场景），再查找正在运行的可执行文件旁边或其上级目录，
// 使安装后的二进制也能找到它的 migrations。
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

// HealthChecker 将 GORM 连接适配到 API 健康检查端点。
type HealthChecker struct {
	DB *gorm.DB
}

// Ping 报告底层连接池是否能响应 ping。
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
