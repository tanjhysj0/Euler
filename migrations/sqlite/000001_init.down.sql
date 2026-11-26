-- 回滚前需关闭外键约束：tool_calls 与 approvals 互为外键，
-- 开启外键时无法删除其中任意一张表。
PRAGMA foreign_keys=OFF;
DROP TABLE IF EXISTS run_events;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS model_profiles;
