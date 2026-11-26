package persistence

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	// PostgreSQL 是独立服务进程，支持多连接并发读写。
	// 连接池大小按典型本地/小规模部署设定。
	postgresMaxOpenConns    = 10
	postgresMaxIdleConns    = 5
	postgresConnMaxLifetime = 30 * time.Minute
)

// postgresDialect 是 PostgreSQL 方言实现（基于 gorm.io/driver/postgres，
// 底层驱动为 jackc/pgx）。
type postgresDialect struct{}

// init 把 PostgreSQL 方言注册到全局注册表。
func init() { Register("postgres", postgresDialect{}) }

// GormDialector 返回 gorm.io/driver/postgres 的 GORM Dialector。
func (d postgresDialect) GormDialector(databaseURL string) gorm.Dialector {
	return postgres.Open(d.BuildDSN(databaseURL))
}

// SQLDriver 返回原生 database/sql 驱动名（pgx 注册为 "pgx"）。
func (postgresDialect) SQLDriver() string { return "pgx" }

// BuildDSN 直接返回 databaseURL：PostgreSQL 的连接串已包含全部参数
// （host/port/dbname/sslmode 等），无需追加方言特定项。
func (postgresDialect) BuildDSN(databaseURL string) string { return databaseURL }

// Prepare 返回 nil：PostgreSQL 是独立服务进程，无需本地目录准备。
func (postgresDialect) Prepare(databaseURL string) error { return nil }

// PoolConfig 返回 PostgreSQL 推荐的多连接池配置。
func (postgresDialect) PoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    postgresMaxOpenConns,
		MaxIdleConns:    postgresMaxIdleConns,
		ConnMaxLifetime: postgresConnMaxLifetime,
	}
}

// GormPlugins 返回 nil：PostgreSQL 的 timestamptz 自带时区处理，
// 无需 SQLite 那样的 UTC 文本规范化插件。
func (postgresDialect) GormPlugins() []gorm.Plugin { return nil }

// MigrationsDir 返回 PostgreSQL 方言的迁移文件目录。
func (postgresDialect) MigrationsDir() string { return "migrations/postgres" }

// CaseInsensitiveLike 返回 ILIKE（PostgreSQL 特有的大小写不敏感 LIKE）。
func (postgresDialect) CaseInsensitiveLike() string { return "ILIKE" }

// EnsureWorkspace 在事务内根据 path 取出或创建工作区。
// 用 pg_advisory_xact_lock 对 path 加事务级咨询锁，保证同 path 的事务串行，
// 因此无需 SQLite 那样的 UNIQUE 冲突重试。
func (postgresDialect) EnsureWorkspace(tx *gorm.DB, path string) (Workspace, error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", path).Error; err != nil {
		return Workspace{}, fmt.Errorf("lock workspace: %w", err)
	}
	var row Workspace
	err := tx.Where("path = ?", path).First(&row).Error
	switch {
	case err == nil:
		return row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = Workspace{ID: uuid.New(), Path: path}
		if createErr := tx.Create(&row).Error; createErr != nil {
			return Workspace{}, fmt.Errorf("create workspace: %w", createErr)
		}
		return row, nil
	default:
		return Workspace{}, fmt.Errorf("lock workspace: %w", err)
	}
}
