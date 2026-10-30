package host

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Store 把主机清单与设置持久化到 SQLite（纯 Go 驱动，无 CGO，
// 保证 Windows/macOS 桌面构建无工具链负担）。
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS hosts (
	id        TEXT PRIMARY KEY,
	name      TEXT NOT NULL UNIQUE,
	addr      TEXT NOT NULL,
	port      INTEGER NOT NULL,
	user      TEXT NOT NULL,
	auth_json TEXT NOT NULL,
	tags_json TEXT NOT NULL,
	os        TEXT NOT NULL DEFAULT '',
	folder    TEXT NOT NULL DEFAULT '',
	owner     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	username   TEXT PRIMARY KEY,
	pw_hash    TEXT NOT NULL,
	created_at TEXT NOT NULL,
	role       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS secrets (
	ref TEXT PRIMARY KEY,
	ct  BLOB NOT NULL
);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// modernc.org/sqlite 无默认 busy_timeout，而 database/sql 默认开无界连接池，
	// 多连接并发写会立刻报 SQLITE_BUSY "database is locked"。单进程桌面场景
	// 用单连接把所有读写串行化，一行消除锁竞争。
	db.SetMaxOpenConns(1)
	// WAL：写不再阻塞读（busy_timeout 下读写排队语义更平滑）。单实例
	// 假设不变（评审 ③-5：只解读写并发形态，不引入多实例）。
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS 不改旧库：补列迁移，列已存在则忽略（幂等）。
	if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN folder TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("migrate folder column: %w", err)
		}
	}
	// users.role 同模式幂等补列（旧库 CLI 建的用户默认普通角色）。
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("migrate users role column: %w", err)
		}
	}
	// hosts.owner 同模式幂等补列（设计 §3.2：空值归一为 admin 归属，
	// 不在迁移时刷数据——读侧 ListFor 归一）。
	if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN owner TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("migrate owner column: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) List() ([]*Host, error) {
	rows, err := s.db.Query(`SELECT id, name, addr, port, user, auth_json, tags_json, os, folder, owner FROM hosts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	return scanHosts(rows)
}

// scanHosts 是 List/ListFor 共用的行扫描循环。
func scanHosts(rows *sql.Rows) ([]*Host, error) {
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		h := &Host{}
		var authJSON, tagsJSON string
		if err := rows.Scan(&h.ID, &h.Name, &h.Addr, &h.Port, &h.User, &authJSON, &tagsJSON, &h.OS, &h.Folder, &h.Owner); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(authJSON), &h.Auth); err != nil {
			return nil, fmt.Errorf("host %s auth: %w", h.Name, err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &h.Tags); err != nil {
			return nil, fmt.Errorf("host %s tags: %w", h.Name, err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListFor 返回该视角可见的主机：普通用户只看自己的（owner = username）；
// admin 看全表。空 owner 行归一为 admin 归属——仅 admin 视角可见（存量
// 迁行语义，设计 §3.2）。ORDER BY name 与 List 一致。
func (s *Store) ListFor(owner string, isAdmin bool) ([]*Host, error) {
	q := `SELECT id, name, addr, port, user, auth_json, tags_json, os, folder, owner FROM hosts`
	args := []any{}
	if !isAdmin && owner != "" {
		q += ` WHERE owner = ?`
		args = append(args, owner)
	}
	q += ` ORDER BY name`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list hosts for %s: %w", owner, err)
	}
	return scanHosts(rows)
}

// ReassignHosts 把 from 名下全部主机划给 to（删除用户时移交 admin 用）。
func (s *Store) ReassignHosts(from, to string) error {
	if _, err := s.db.Exec(`UPDATE hosts SET owner = ? WHERE owner = ?`, to, from); err != nil {
		return fmt.Errorf("reassign hosts %s→%s: %w", from, to, err)
	}
	return nil
}

// Save 按 ID upsert。Name 有 UNIQUE 约束，重名返回错误。
func (s *Store) Save(h *Host) error {
	h.Folder = NormalizeFolder(h.Folder)
	authJSON, err := json.Marshal(h.Auth)
	if err != nil {
		return fmt.Errorf("host %s auth encode: %w", h.ID, err)
	}
	tagsJSON, err := json.Marshal(h.Tags)
	if err != nil {
		return fmt.Errorf("host %s tags encode: %w", h.ID, err)
	}
	if _, err := s.db.Exec(`INSERT INTO hosts (id, name, addr, port, user, auth_json, tags_json, os, folder, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, addr=excluded.addr, port=excluded.port,
			user=excluded.user, auth_json=excluded.auth_json, tags_json=excluded.tags_json,
			os=excluded.os, folder=excluded.folder, owner=excluded.owner`,
		h.ID, h.Name, h.Addr, h.Port, h.User, string(authJSON), string(tagsJSON), h.OS, h.Folder, h.Owner); err != nil {
		return fmt.Errorf("save host %s: %w", h.ID, err)
	}
	return nil
}

func (s *Store) Delete(id string) error {
	if _, err := s.db.Exec(`DELETE FROM hosts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete host %s: %w", id, err)
	}
	return nil
}

func (s *Store) SetSetting(key, value string) error {
	if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// GetSetting 返回设置值；键不存在时返回 ("", nil)。
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// ---- 用户（web 登录） ----

// ErrNoSuchUser 表示用户名不存在（登录失败判定用）。
var ErrNoSuchUser = errors.New("no such user")

// PwHashCost 是登录密码 bcrypt 成本（webapi dummy-hash 均衡与
// gengar-server --add-user 必须同源引用，改动其一即破坏时序均衡）。
const PwHashCost = 12

// User 是 web 登录账号。PwHash 为 bcrypt 散列，明文密码不落库。
type User struct {
	Username  string
	PwHash    string
	CreatedAt string
	Role      string // "admin" 或 ""（普通用户）
}

func (s *Store) CreateUser(u User) error {
	if _, err := s.db.Exec(`INSERT INTO users (username, pw_hash, created_at, role) VALUES (?, ?, ?, ?)`,
		u.Username, u.PwHash, u.CreatedAt, u.Role); err != nil {
		return fmt.Errorf("create user %s: %w", u.Username, err)
	}
	return nil
}

// GetUser 返回用户；不存在时返回 ErrNoSuchUser（errors.Is 判定）。
func (s *Store) GetUser(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT username, pw_hash, created_at, role FROM users WHERE username = ?`, username).
		Scan(&u.Username, &u.PwHash, &u.CreatedAt, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get user %s: %w", username, ErrNoSuchUser)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListUsers 返回全部用户（按用户名排序，用户管理列表用）。
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT username, pw_hash, created_at, role FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.Username, &u.PwHash, &u.CreatedAt, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserPassword 重置密码哈希；用户不存在返回 ErrNoSuchUser。
func (s *Store) UpdateUserPassword(username, pwHash string) error {
	res, err := s.db.Exec(`UPDATE users SET pw_hash = ? WHERE username = ?`, pwHash, username)
	if err != nil {
		return fmt.Errorf("update password %s: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update password %s: %w", username, ErrNoSuchUser)
	}
	return nil
}

// SetUserRole 设置角色；用户不存在返回 ErrNoSuchUser。
func (s *Store) SetUserRole(username, role string) error {
	res, err := s.db.Exec(`UPDATE users SET role = ? WHERE username = ?`, role, username)
	if err != nil {
		return fmt.Errorf("set role %s: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set role %s: %w", username, ErrNoSuchUser)
	}
	return nil
}

// DeleteUser 删除用户；用户不存在返回 ErrNoSuchUser。
func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("delete user %s: %w", username, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delete user %s: %w", username, ErrNoSuchUser)
	}
	return nil
}

// ---- 秘密密文（EncryptedStore 的落盘行） ----

// SetSecretBlob upsert 一条密文（EncryptedStore 加密后的产物，本层不感知语义）。
func (s *Store) SetSecretBlob(ref string, ct []byte) error {
	if _, err := s.db.Exec(`INSERT INTO secrets (ref, ct) VALUES (?, ?)
		ON CONFLICT(ref) DO UPDATE SET ct=excluded.ct`, ref, ct); err != nil {
		return fmt.Errorf("set secret %s: %w", ref, err)
	}
	return nil
}

// GetSecretBlob 返回密文；ref 不存在时返回 (nil, nil)。
func (s *Store) GetSecretBlob(ref string) ([]byte, error) {
	var ct []byte
	err := s.db.QueryRow(`SELECT ct FROM secrets WHERE ref = ?`, ref).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ct, err
}

// DeleteSecretBlob 幂等删除。
func (s *Store) DeleteSecretBlob(ref string) error {
	if _, err := s.db.Exec(`DELETE FROM secrets WHERE ref = ?`, ref); err != nil {
		return fmt.Errorf("delete secret %s: %w", ref, err)
	}
	return nil
}
