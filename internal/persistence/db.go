// Package persistence 负责数据库方言抽象、schema、实体和连接建立。
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	migrateTimeout = 30 * time.Second
	// invalidMigrationVersion 是迁移文件名无法解析版本号时的占位返回值，
	// 调用方只在 ok 为 true 时才使用版本号。
	invalidMigrationVersion = 0
)

// migrationFileRE 从迁移文件名中提取版本号前缀，例如 000001_init.up.sql → 1。
var migrationFileRE = regexp.MustCompile(`^(\d+)_.+\.up\.sql$`)

// Open 按方言名连接到数据库，注册方言插件，配置连接池并验证连通性。
// driverName 必须是已注册的方言名（如 "sqlite"、"postgres"）；
// 为空时自动从 databaseURL 推断（见 InferDriver）。
func Open(ctx context.Context, driverName, databaseURL string) (*gorm.DB, error) {
	if driverName == "" {
		driverName = InferDriver(databaseURL)
	}
	dialect, err := prepareDialect(driverName, databaseURL)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(dialect.GormDialector(databaseURL), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
		// 自动填充的时间字段（如 created_at）统一使用 UTC，
		// 与显式写入经方言插件规范化后的结果保持一致。
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driverName, err)
	}
	for _, plugin := range dialect.GormPlugins() {
		if err = db.Use(plugin); err != nil {
			return nil, fmt.Errorf("register plugin %s: %w", plugin.Name(), err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	dialect.PoolConfig().Apply(sqlDB)
	if err = sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping %s: %w", driverName, err)
	}
	return db, nil
}

// prepareDialect 查找方言并做打开前准备（如 SQLite 创建父目录）。
// 抽出以减少 Open/Migrate 的分支数。
func prepareDialect(driverName, databaseURL string) (Dialect, error) {
	dialect, err := Lookup(driverName)
	if err != nil {
		return nil, err
	}
	if err = dialect.Prepare(databaseURL); err != nil {
		return nil, err
	}
	return dialect, nil
}

// openMigrateDB 建立用于迁移的原生 database/sql 连接。
func openMigrateDB(dialect Dialect, databaseURL string) (*sql.DB, error) {
	sqlDB, err := sql.Open(dialect.SQLDriver(), dialect.BuildDSN(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("open %s for migrate: %w", dialect.SQLDriver(), err)
	}
	dialect.PoolConfig().Apply(sqlDB)
	return sqlDB, nil
}

// Migrate 应用方言迁移目录中所有待执行的 up 迁移。
// 每个迁移文件在独立事务中执行，并在 schema_migrations 表中记录
// 已应用版本号，保证幂等可重复执行。driverName 为空时自动从 URL 推断。
func Migrate(driverName, databaseURL string) error {
	if driverName == "" {
		driverName = InferDriver(databaseURL)
	}
	dialect, err := prepareDialect(driverName, databaseURL)
	if err != nil {
		return err
	}
	dir, err := migrationsDir(dialect.MigrationsDir())
	if err != nil {
		return fmt.Errorf("locate migrations: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	slices.Sort(matches)

	sqlDB, err := openMigrateDB(dialect, databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrateDB(sqlDB)

	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	if err = sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", driverName, err)
	}
	if err = ensureMigrationsTable(ctx, sqlDB); err != nil {
		return err
	}

	for _, path := range matches {
		if err := processMigration(ctx, sqlDB, path); err != nil {
			return err
		}
	}
	return nil
}

// processMigration 解析单个迁移文件的版本号，若尚未应用则读取并执行。
// 文件名不匹配迁移命名规则时静默跳过。
func processMigration(ctx context.Context, sqlDB *sql.DB, path string) error {
	version, ok := migrationVersion(filepath.Base(path))
	if !ok {
		return nil
	}
	applied, err := isMigrationApplied(ctx, sqlDB, version)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	mf, err := readMigrationFile(path, version)
	if err != nil {
		return err
	}
	return applyMigration(ctx, sqlDB, mf)
}

// closeMigrateDB 关闭迁移用连接，关闭失败仅记录告警，不影响迁移结果。
func closeMigrateDB(sqlDB *sql.DB) {
	if err := sqlDB.Close(); err != nil {
		slog.Warn("close migrate db", "error", err)
	}
}

// ensureMigrationsTable 创建版本记录表（若不存在）。
func ensureMigrationsTable(ctx context.Context, sqlDB *sql.DB) error {
	if _, err := sqlDB.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	return nil
}

// isMigrationApplied 判断指定版本是否已记录到 schema_migrations。
func isMigrationApplied(ctx context.Context, sqlDB *sql.DB, version int) (bool, error) {
	var marker int
	err := sqlDB.QueryRowContext(ctx,
		"SELECT 1 FROM schema_migrations WHERE version = ? LIMIT 1", version,
	).Scan(&marker)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check migration %d: %w", version, err)
}

// migrationFile 封装单个迁移的元数据与内容，作为 applyMigration 的参数对象，
// 避免形参数量超过限制。
type migrationFile struct {
	version int
	name    string
	content []byte
}

// readMigrationFile 读取迁移文件内容并封装为 migrationFile。
func readMigrationFile(path string, version int) (migrationFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return migrationFile{}, fmt.Errorf("read migration %d: %w", version, err)
	}
	return migrationFile{version: version, name: filepath.Base(path), content: content}, nil
}

// applyMigration 在单个事务中执行一个迁移文件，并记录版本号。
func applyMigration(ctx context.Context, sqlDB *sql.DB, mf migrationFile) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", mf.version, err)
	}
	if _, err = tx.ExecContext(ctx, string(mf.content)); err != nil {
		rollbackTx(tx, mf.version)
		return fmt.Errorf("apply migration %d (%s): %w", mf.version, mf.name, err)
	}
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version) VALUES (?)", mf.version); err != nil {
		rollbackTx(tx, mf.version)
		return fmt.Errorf("record migration %d: %w", mf.version, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", mf.version, err)
	}
	return nil
}

// rollbackTx 回滚事务，回滚失败仅记录告警，避免覆盖原始的迁移错误。
func rollbackTx(tx *sql.Tx, version int) {
	if err := tx.Rollback(); err != nil {
		slog.Warn("rollback migration", "version", version, "error", err)
	}
}

// migrationVersion 从迁移文件名解析版本号。
func migrationVersion(name string) (int, bool) {
	m := migrationFileRE.FindStringSubmatch(name)
	if m == nil {
		return invalidMigrationVersion, false
	}
	v, err := strconv.Atoi(m[1])
	return v, err == nil
}

// migrationsDir 定位方言迁移目录：先查找工作目录（仓库检出和测试场景），
// 再查找正在运行的可执行文件旁边或其上级目录，使安装后的二进制也能找到。
func migrationsDir(dialectDir string) (string, error) {
	candidates := []string{dialectDir}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, dialectDir),
			filepath.Join(exeDir, "..", dialectDir))
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
	return "", errors.New("migrations directory not found: " + dialectDir)
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
		return fmt.Errorf("ping db: %w", err)
	}
	return nil
}
