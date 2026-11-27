//go:build !aix

package persistence

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestLookupRegisteredDialects 验证两个内置方言都已通过 init 注册。
func TestLookupRegisteredDialects(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		d, err := Lookup(name)
		require.NoError(t, err, name)
		require.NotNil(t, d, name)
	}
}

// TestLookupUnknownDialectReturnsError 验证未注册的方言名会报错。
func TestLookupUnknownDialectReturnsError(t *testing.T) {
	_, err := Lookup("mysql")
	require.Error(t, err)
}

// TestInferDriver 验证从 databaseURL 推断方言的规则。
func TestInferDriver(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@127.0.0.1/db":   "postgres",
		"postgresql://u:p@127.0.0.1/db": "postgres",
		"/tmp/euler.db":                 "sqlite",
		"file:euler.db":                 "sqlite",
		"~/.euler/euler.db":             "sqlite",
	}
	for url, want := range cases {
		require.Equal(t, want, InferDriver(url), url)
	}
}

// TestSqliteDialectBuildDSN 验证 SQLite 的 DSN 会追加 PRAGMA 参数。
func TestSqliteDialectBuildDSN(t *testing.T) {
	d := sqliteDialect{}
	dsn := d.BuildDSN("/tmp/euler.db")
	require.Contains(t, dsn, "busy_timeout")
	require.Contains(t, dsn, "journal_mode(WAL)")
	require.Contains(t, dsn, "foreign_keys(1)")
	// 已带 query 的 URL 应用 & 连接而非 ?。
	dsn = d.BuildDSN("file:euler.db?mode=rwc")
	require.Contains(t, dsn, "mode=rwc&_pragma=")
}

// TestPostgresDialectBuildDSN 验证 PG 的 DSN 原样透传。
func TestPostgresDialectBuildDSN(t *testing.T) {
	d := postgresDialect{}
	const url = "postgres://u:p@127.0.0.1/db?sslmode=disable"
	require.Equal(t, url, d.BuildDSN(url))
}

// TestDialectPoolConfig 验证两个方言的连接池参数差异。
func TestDialectPoolConfig(t *testing.T) {
	sqliteCfg := sqliteDialect{}.PoolConfig()
	require.Equal(t, sqliteMaxOpenConns, sqliteCfg.MaxOpenConns)
	pgCfg := postgresDialect{}.PoolConfig()
	require.Equal(t, postgresMaxOpenConns, pgCfg.MaxOpenConns)
}

// TestDialectCaseInsensitiveLike 验证 LIKE 操作符的方言差异。
func TestDialectCaseInsensitiveLike(t *testing.T) {
	require.Equal(t, "LIKE", sqliteDialect{}.CaseInsensitiveLike())
	require.Equal(t, "ILIKE", postgresDialect{}.CaseInsensitiveLike())
}

// TestDialectMigrationsDir 验证迁移目录按方言分离。
func TestDialectMigrationsDir(t *testing.T) {
	require.Equal(t, "migrations/sqlite", sqliteDialect{}.MigrationsDir())
	require.Equal(t, "migrations/postgres", postgresDialect{}.MigrationsDir())
}

// TestFromGorm 验证从 *gorm.DB 反查方言。
func TestFromGorm(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	d, err := FromGorm(db)
	require.NoError(t, err)
	require.Equal(t, "migrations/sqlite", d.MigrationsDir())
}

// TestFromGormNilReturnsError 验证 nil db 会报错而非 panic。
func TestFromGormNilReturnsError(t *testing.T) {
	_, err := FromGorm(nil)
	require.Error(t, err)
}
