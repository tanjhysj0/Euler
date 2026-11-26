-- SQLite 3.35+ 支持 DROP COLUMN（modernc.org/sqlite 捆绑的版本满足要求）。
ALTER TABLE messages DROP COLUMN parts;
