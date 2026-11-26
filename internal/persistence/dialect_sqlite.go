package persistence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// SQLite 是文件型数据库，写操作天然串行化。单连接池可彻底避免
	// "database is locked"（SQLITE_BUSY）问题；本应用为本地单用户场景，
	// 实时事件流走内存 hub，不依赖并发读，因此单连接不会成为瓶颈。
	sqliteMaxOpenConns    = 1
	sqliteMaxIdleConns    = 1
	sqliteConnMaxLifetime = 30 * time.Minute
	// emptyString 避免空字符串字面量被 add-constant 反复提示。
	emptyString = ""
	// sqliteDirPerm 是创建数据库父目录的权限（仅所有者可访问，
	// 与工作区私有权限一致；euler.db 含模型密钥等敏感数据）。
	sqliteDirPerm = 0o700
)

// sqliteDialect 是 SQLite 方言实现（基于纯 Go 的 glebarez/sqlite 驱动，
// 兼容 CGO_ENABLED=0 的 euler-agent 构建）。
type sqliteDialect struct{}

// init 把 SQLite 方言注册到全局注册表。
func init() { Register("sqlite", sqliteDialect{}) }

// GormDialector 返回 glebarez/sqlite 的 GORM Dialector。
func (d sqliteDialect) GormDialector(databaseURL string) gorm.Dialector {
	return sqlite.Open(d.BuildDSN(databaseURL))
}

// SQLDriver 返回原生 database/sql 驱动名（glebarez 注册为 "sqlite"）。
func (sqliteDialect) SQLDriver() string { return "sqlite" }

// BuildDSN 在 databaseURL 上追加 SQLite 连接参数：
//   - busy_timeout：遇到锁时最多等待 5 秒，避免瞬时锁失败；
//   - journal_mode=WAL：允许读写并发，提升本地交互体验；
//   - foreign_keys=1：开启外键约束校验（SQLite 默认关闭）。
//
// databaseURL 可以是裸文件路径（/path/to/euler.db）或 file: URI。
func (sqliteDialect) BuildDSN(databaseURL string) string {
	pragmas := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	if strings.Contains(databaseURL, "?") {
		return databaseURL + "&" + pragmas
	}
	return databaseURL + "?" + pragmas
}

// Prepare 在打开连接前创建 SQLite 数据库文件的父目录。
// SQLite 不会自动创建父目录，缺失时会报 "unable to open database file"。
// 对 :memory:、空路径和当前目录下的文件不做事。
func (sqliteDialect) Prepare(databaseURL string) error {
	path := sqliteFilePath(databaseURL)
	if path == emptyString {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == emptyString || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, sqliteDirPerm); err != nil {
		return fmt.Errorf("create sqlite parent dir %s: %w", dir, err)
	}
	return nil
}

// sqliteFilePath 从 databaseURL 提取数据库文件路径
// （去掉 file: 前缀和 query 部分）。:memory: 与空串返回空。
func sqliteFilePath(databaseURL string) string {
	s := strings.TrimPrefix(databaseURL, "file:")
	// 去掉 query 部分（? 之后）；Cut 未找到时 before 即整个串。
	s, _, _ = strings.Cut(s, "?")
	if s == emptyString || s == ":memory:" {
		return emptyString
	}
	return s
}

// PoolConfig 返回 SQLite 推荐的单连接池配置。
func (sqliteDialect) PoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    sqliteMaxOpenConns,
		MaxIdleConns:    sqliteMaxIdleConns,
		ConnMaxLifetime: sqliteConnMaxLifetime,
	}
}

// GormPlugins 返回 SQLite 需要的 UTC 规范化插件。
// SQLite 没有独立时间类型，时间以文本存储并按文本排序，统一写入 UTC
// 才能让文本序与时间序一致。
func (sqliteDialect) GormPlugins() []gorm.Plugin {
	return []gorm.Plugin{utcNormalizer{}}
}

// MigrationsDir 返回 SQLite 方言的迁移文件目录。
func (sqliteDialect) MigrationsDir() string { return "migrations/sqlite" }

// CaseInsensitiveLike 返回 LIKE（SQLite 的 LIKE 默认对 ASCII 大小写不敏感）。
func (sqliteDialect) CaseInsensitiveLike() string { return "LIKE" }

// EnsureWorkspace 在事务内根据 path 取出或创建工作区。
// SQLite 的 SELECT ... FOR UPDATE 会被解析但忽略；真正的并发安全由
// workspaces.path 的唯一约束 + 冲突重试保证。
func (sqliteDialect) EnsureWorkspace(tx *gorm.DB, path string) (Workspace, error) {
	var row Workspace
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("path = ?", path).
		First(&row).Error
	switch {
	case err == nil:
		return row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = Workspace{ID: uuid.New(), Path: path}
		if createErr := tx.Create(&row).Error; createErr != nil {
			// 并发创建同一路径时，后到的事务会命中唯一约束冲突，
			// 此时重读已存在的行即可，保证同一路径只对应一个 workspace。
			if errors.Is(createErr, gorm.ErrDuplicatedKey) {
				if reselectErr := tx.Where("path = ?", path).First(&row).Error; reselectErr != nil {
					return Workspace{}, fmt.Errorf("create workspace: %w", reselectErr)
				}
				return row, nil
			}
			return Workspace{}, fmt.Errorf("create workspace: %w", createErr)
		}
		return row, nil
	default:
		return Workspace{}, fmt.Errorf("lock workspace: %w", err)
	}
}
