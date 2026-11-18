package antiaccount

import (
	"database/sql"
	"fmt"
	"io"
	"os"

	_ "modernc.org/sqlite"

	"cursor-inner/internal/i18n"
)

// state.vscdb 是 VS Code 系编辑器的全局状态存储：单表 ItemTable(key TEXT UNIQUE,
// value BLOB)。用 pure-Go 的 modernc.org/sqlite 读写，不引入 CGO。
//
// 改凭据前先把库收成单文件并复制为 state.vscdb.cursor-inner-bak，再写入。
// IDE 必须先退出（上层负责）：退出钩子可能把旧凭据写回来，锁还在时写入会失败。

var errSQLiteFormat = fmt.Errorf("state.vscdb: unexpected sqlite layout")

type sqliteDB struct {
	path string
	db   *sql.DB
}

// Get 按 key 查 value。没找到返回 (nil, false)。
func (s *sqliteDB) Get(key string) ([]byte, bool) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM ItemTable WHERE key=?`, key).Scan(&v)
	if err != nil {
		return nil, false
	}
	return v, true
}

// Put 覆盖一个已存在的 key。key 不存在时报错。
func (s *sqliteDB) Put(key string, value []byte) error {
	res, err := s.db.Exec(`UPDATE ItemTable SET value=? WHERE key=?`, value, key)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: key %q not found", errSQLiteFormat, key)
	}
	return nil
}

func (s *sqliteDB) Close() error { return s.db.Close() }

func (s *sqliteDB) checkpoint() error {
	if _, err := s.db.Exec(`PRAGMA busy_timeout=2000`); err != nil {
		return err
	}
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// readStateDB 打开 state.vscdb（读写模式）。
func readStateDB(path string) (*sqliteDB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(`PRAGMA busy_timeout=2000`); err != nil {
		d.Close()
		return nil, err
	}
	var name string
	err = d.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ItemTable'`).Scan(&name)
	if err != nil {
		d.Close()
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: ItemTable not found", errSQLiteFormat)
		}
		return nil, fmt.Errorf("%w: %v", errSQLiteFormat, err)
	}
	return &sqliteDB{path: path, db: d}, nil
}

// snapshotStateDB 把当前库收成单文件、关闭连接，再复制一份备份。
// 必须在修改之前调用。关闭后再复制，避免 Windows 上文件仍被本进程锁住。
func snapshotStateDB(s *sqliteDB) error {
	path := s.path
	if err := s.checkpoint(); err != nil {
		_ = s.Close()
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	if err := copyFileMode(path, path+".cursor-inner-bak", 0o600); err != nil {
		return i18n.Wrap("备份 state.vscdb 失败：", "Backing up state.vscdb failed: ", err)
	}
	return nil
}

// writeStateDB 把 WAL 合并回主文件并关闭连接。调用方已经把修改写进库。
func writeStateDB(path string, s *sqliteDB) error {
	if err := s.checkpoint(); err != nil {
		_ = s.Close()
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	return s.Close()
}

func upsertKey(tx *sql.Tx, key string, value []byte) error {
	_, err := tx.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	return nil
}

func deleteKey(tx *sql.Tx, key string) error {
	_, err := tx.Exec(`DELETE FROM ItemTable WHERE key=?`, key)
	if err != nil {
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
