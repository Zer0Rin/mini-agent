// Package store 会话与消息的持久化。
//
// 设计要点：
//   - 所有方法都带 userID，查询一律按用户过滤 —— 从接口层面杜绝越权
//     （nexus-agent 记忆模块按 ID 直接改数据的教训）。
//   - 消息组织成一棵树：每条消息记录 parent_id，会话的 head 指向当前分支的末端。
//     从任意一条历史消息分叉重新对话，旧分支不会丢（参考 pi 的会话树）。
//   - Store 是接口：先用 SQLite，以后换 PostgreSQL 只需新增一个实现，
//     并用 storetest 里的同一套契约测试验证。
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ErrNotFound 记录不存在，或不属于该用户（两种情况不区分，避免泄露"别人的数据存在"）。
var ErrNotFound = errors.New("store: not found")

// Session 一个会话。
type Session struct {
	ID        string
	UserID    string
	Title     string
	HeadID    string // 当前分支末端的消息 ID；空表示还没有消息
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Record 一条已保存的消息。
type Record struct {
	ID        string
	SessionID string
	ParentID  string // 空表示根消息
	Message   llm.Message
	CreatedAt time.Time
}

// Store 会话存储。实现必须并发安全。
type Store interface {
	CreateSession(ctx context.Context, userID, title string) (Session, error)
	GetSession(ctx context.Context, userID, sessionID string) (Session, error)
	// ListSessions 按最近更新时间倒序。
	ListSessions(ctx context.Context, userID string, limit int) ([]Session, error)
	RenameSession(ctx context.Context, userID, sessionID, title string) error
	// DeleteSession 删除会话及其全部消息。
	DeleteSession(ctx context.Context, userID, sessionID string) error

	// AppendMessage 在 parentID 之后追加一条消息，并把 head 移到新消息。
	// parentID 为空表示根；非空时必须是同一会话里的消息，否则返回 ErrNotFound。
	AppendMessage(ctx context.Context, userID, sessionID, parentID string, m llm.Message) (Record, error)
	// Path 返回当前分支：从根到 head 的全部消息（按时间正序）。
	Path(ctx context.Context, userID, sessionID string) ([]Record, error)
	// SetHead 把 head 移到某条历史消息上，之后追加的消息会长出一个新分支。
	// messageID 为空表示回到会话开头。
	SetHead(ctx context.Context, userID, sessionID, messageID string) error

	Close() error
}

// NewID 生成 16 字节随机 ID（32 位十六进制）。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand 读失败说明系统随机源坏了，无法继续
	}
	return hex.EncodeToString(b[:])
}
