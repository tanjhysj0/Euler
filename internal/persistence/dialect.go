package persistence

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Dialect 抽象一种数据库方言的所有可变点。
//
// 开闭原则：新增数据库只需实现本接口并在 init 中 Register，
// 不需要修改任何现有文件。每个方法对应一个真实的方言差异点。
type Dialect interface {
	// GormDialector 把 databaseURL 转为 GORM Dialector。
	GormDialector(databaseURL string) gorm.Dialector

	// SQLDriver 返回原生 database/sql 驱动名（用于 Migrate 的原生连接）。
	SQLDriver() string

	// BuildDSN 把 databaseURL 转为驱动 DSN（追加方言特定连接参数）。
	BuildDSN(databaseURL string) string

	// Prepare 在打开连接前做方言特定的准备工作。
	// SQLite 创建数据库文件的父目录；服务端方言（如 PG）返回 nil。
	Prepare(databaseURL string) error

	// PoolConfig 返回方言推荐的连接池参数。
	PoolConfig() PoolConfig

	// GormPlugins 返回方言需要的 GORM 插件（如 SQLite 的 UTC 规范化）。
	// 不需要时返回 nil。
	GormPlugins() []gorm.Plugin

	// MigrationsDir 返回该方言的迁移文件目录（相对仓库根）。
	MigrationsDir() string

	// CaseInsensitiveLike 返回大小写不敏感的 LIKE 操作符。
	// PostgreSQL 为 ILIKE，SQLite/MySQL 为 LIKE。
	CaseInsensitiveLike() string

	// EnsureWorkspace 在事务内根据 path 取出或创建工作区，
	// 处理方言特定的并发策略（PG 用 advisory lock；SQLite 走 UNIQUE+冲突重试）。
	EnsureWorkspace(tx *gorm.DB, path string) (Workspace, error)
}

// PoolConfig 描述方言推荐的连接池参数。
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Apply 把连接池参数应用到 *sql.DB。
func (c PoolConfig) Apply(sqlDB *sql.DB) {
	sqlDB.SetMaxOpenConns(c.MaxOpenConns)
	sqlDB.SetMaxIdleConns(c.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(c.ConnMaxLifetime)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Dialect{}
)

// Register 把方言注册到全局注册表。在方言文件的 init 中调用。
// 同名重复注册会 panic（与 database/sql 的 Register 行为一致）。
func Register(name string, d Dialect) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("persistence: dialect %q already registered", name))
	}
	registry[name] = d
}

// Lookup 按名字取出方言；未注册的名字返回错误。
func Lookup(name string) (Dialect, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("persistence: unknown dialect %q", name)
	}
	return d, nil
}

// FromGorm 从 *gorm.DB 反查方言（利用 db.Dialector.Name()）。
// 让 NewService 等构造函数无需显式接收方言参数。
func FromGorm(db *gorm.DB) (Dialect, error) {
	if db == nil || db.Dialector == nil {
		return nil, errors.New("persistence: nil gorm db or dialector")
	}
	return Lookup(db.Name())
}

// InferDriver 从 databaseURL 推断方言名。
// "postgres://" 或 "postgresql://" 前缀 → postgres；其他（文件路径、file: URI）→ sqlite。
// Open/Migrate 的 driverName 为空时用它自动推断。
func InferDriver(databaseURL string) string {
	if strings.HasPrefix(databaseURL, "postgres://") ||
		strings.HasPrefix(databaseURL, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}
