package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// SQLite 基于 database/sql 的实现。驱动由调用方导入（推荐纯 Go 的 modernc.org/sqlite，免 CGO），
// 本包不依赖任何具体驱动。
type SQLite struct {
	db *sql.DB
}

// migrations 按版本顺序执行；已执行过的版本会跳过（幂等）。
// 每个版本是一组语句，逐条执行（不依赖驱动是否支持"一次执行多条语句"）。
// 新增表结构时只能往末尾追加新版本，不要修改已经发布的版本。
var migrations = [][]string{
	// v1：会话与消息树
	{
		`CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			title      TEXT NOT NULL,
			head_id    TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user_updated ON sessions(user_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id            TEXT PRIMARY KEY,
			session_id    TEXT NOT NULL,
			parent_id     TEXT NOT NULL DEFAULT '',
			role          TEXT NOT NULL,
			content       TEXT NOT NULL,          -- 冗余一份正文，方便以后做全文检索
			data          TEXT NOT NULL,          -- 完整消息（JSON）
			input_tokens  INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id)`,
	},
}

// NewSQLite 在已打开的连接上初始化存储并执行迁移。
// SQLite 同一时刻只允许一个写者，这里把连接池限制为 1，避免 "database is locked"。
func NewSQLite(ctx context.Context, db *sql.DB) (*SQLite, error) {
	db.SetMaxOpenConns(1)
	// 这两条 PRAGMA 会返回一行结果，用 QueryRow 读掉，兼容不同驱动
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		var ignored string
		if err := db.QueryRowContext(ctx, pragma).Scan(&ignored); err != nil {
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return &SQLite{db: db}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for i, stmts := range migrations {
		version := i + 1
		var exists int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration v%d: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, nowMS()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func nowMS() int64              { return time.Now().UnixMilli() }
func fromMS(ms int64) time.Time { return time.UnixMilli(ms) }
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// dbMessage 消息在数据库里的 JSON 形态。单独定义而不是直接序列化 llm.Message，
// 这样内部结构改名不会让历史数据读不出来。
type dbMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	Reasoning  string       `json:"reasoning,omitempty"`
	ToolCalls  []dbToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolName   string       `json:"tool_name,omitempty"`
	IsError    bool         `json:"is_error,omitempty"`
	Usage      *llm.Usage   `json:"usage,omitempty"`
}

type dbToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

func encodeMessage(m llm.Message) (string, error) {
	d := dbMessage{Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, ToolCallID: m.ToolCallID,
		ToolName: m.ToolName, IsError: m.IsError, Usage: m.Usage}
	for _, tc := range m.ToolCalls {
		d.ToolCalls = append(d.ToolCalls, dbToolCall{ID: tc.ID, Name: tc.Name, Args: tc.ArgsJSON})
	}
	b, err := json.Marshal(d)
	return string(b), err
}

func decodeMessage(s string) (llm.Message, error) {
	var d dbMessage
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return llm.Message{}, err
	}
	m := llm.Message{Role: d.Role, Content: d.Content, Reasoning: d.Reasoning, ToolCallID: d.ToolCallID,
		ToolName: d.ToolName, IsError: d.IsError, Usage: d.Usage}
	for _, tc := range d.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, ArgsJSON: tc.Args})
	}
	return m, nil
}

// ---- 会话 ----

const sessionCols = `id, user_id, title, head_id, created_at, updated_at`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var se Session
	var created, updated int64
	if err := row.Scan(&se.ID, &se.UserID, &se.Title, &se.HeadID, &created, &updated); err != nil {
		return Session{}, err
	}
	se.CreatedAt, se.UpdatedAt = fromMS(created), fromMS(updated)
	return se, nil
}

func (s *SQLite) CreateSession(ctx context.Context, userID, title string) (Session, error) {
	now := nowMS()
	se := Session{ID: NewID(), UserID: userID, Title: title, CreatedAt: fromMS(now), UpdatedAt: fromMS(now)}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(`+sessionCols+`) VALUES (?, ?, ?, '', ?, ?)`,
		se.ID, userID, title, now, now)
	return se, err
}

func (s *SQLite) GetSession(ctx context.Context, userID, id string) (Session, error) {
	se, err := scanSession(s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id = ? AND user_id = ?`, id, userID))
	return se, notFound(err)
}

func (s *SQLite) ListSessions(ctx context.Context, userID string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = -1 // SQLite：LIMIT -1 表示不限制
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		se, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// execOwned 执行一条带 user_id 条件的写操作；没有命中任何行就返回 ErrNotFound。
func execOwned(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) error {
	res, err := ex.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) RenameSession(ctx context.Context, userID, id, title string) error {
	return execOwned(ctx, s.db, `UPDATE sessions SET title = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		title, nowMS(), id, userID)
}

func (s *SQLite) DeleteSession(ctx context.Context, userID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 成功后再 Rollback 是空操作
	if err := execOwned(ctx, tx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- 消息 ----

func (s *SQLite) AppendMessage(ctx context.Context, userID, sessionID, parentID string, m llm.Message) (Record, error) {
	data, err := encodeMessage(m)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()

	// 会话必须属于该用户
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID).Scan(&one); err != nil {
		return Record{}, notFound(err)
	}
	// 父消息必须在同一会话里
	if parentID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id = ? AND session_id = ?`, parentID, sessionID).Scan(&one); err != nil {
			return Record{}, notFound(err)
		}
	}

	now := nowMS()
	r := Record{ID: NewID(), SessionID: sessionID, ParentID: parentID, Message: m, CreatedAt: fromMS(now)}
	var in, out int
	if m.Usage != nil {
		in, out = m.Usage.InputTokens, m.Usage.OutputTokens
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, session_id, parent_id, role, content, data, input_tokens, output_tokens, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, sessionID, parentID, m.Role, m.Content, data, in, out, now); err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET head_id = ?, updated_at = ? WHERE id = ?`, r.ID, now, sessionID); err != nil {
		return Record{}, err
	}
	return r, tx.Commit()
}

func (s *SQLite) Path(ctx context.Context, userID, sessionID string) ([]Record, error) {
	se, err := s.GetSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if se.HeadID == "" {
		return nil, nil
	}
	// 递归 CTE：从 head 沿 parent_id 一路往上走到根，再按深度倒序输出（根在前）
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE chain(id, parent_id, depth) AS (
			SELECT id, parent_id, 0 FROM messages WHERE id = ? AND session_id = ?
			UNION ALL
			SELECT m.id, m.parent_id, c.depth + 1 FROM messages m JOIN chain c ON m.id = c.parent_id
		)
		SELECT m.id, m.parent_id, m.data, m.created_at
		FROM chain c JOIN messages m ON m.id = c.id
		ORDER BY c.depth DESC`, se.HeadID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var data string
		var created int64
		if err := rows.Scan(&r.ID, &r.ParentID, &data, &created); err != nil {
			return nil, err
		}
		if r.Message, err = decodeMessage(data); err != nil {
			return nil, fmt.Errorf("decode message %s: %w", r.ID, err)
		}
		r.SessionID, r.CreatedAt = sessionID, fromMS(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLite) SetHead(ctx context.Context, userID, sessionID, messageID string) error {
	if messageID != "" {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM messages m JOIN sessions s ON s.id = m.session_id
			WHERE m.id = ? AND m.session_id = ? AND s.user_id = ?`, messageID, sessionID, userID).Scan(&one)
		if err != nil {
			return notFound(err)
		}
	}
	return execOwned(ctx, s.db, `UPDATE sessions SET head_id = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		messageID, nowMS(), sessionID, userID)
}

func (s *SQLite) Close() error { return s.db.Close() }
