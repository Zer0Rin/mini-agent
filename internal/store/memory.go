package store

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// Memory 内存实现：用于测试和本地快速体验，进程退出数据即丢失。
type Memory struct {
	mu       sync.Mutex
	sessions map[string]*Session
	messages map[string]*Record
	now      func() time.Time
}

// NewMemory 创建内存存储。
func NewMemory() *Memory {
	return &Memory{sessions: map[string]*Session{}, messages: map[string]*Record{}, now: time.Now}
}

func (s *Memory) session(userID, id string) (*Session, error) {
	se, ok := s.sessions[id]
	if !ok || se.UserID != userID {
		return nil, ErrNotFound
	}
	return se, nil
}

func (s *Memory) CreateSession(_ context.Context, userID, title string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	se := &Session{ID: NewID(), UserID: userID, Title: title, CreatedAt: now, UpdatedAt: now}
	s.sessions[se.ID] = se
	return *se, nil
}

func (s *Memory) GetSession(_ context.Context, userID, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.session(userID, id)
	if err != nil {
		return Session{}, err
	}
	return *se, nil
}

func (s *Memory) ListSessions(_ context.Context, userID string, limit int) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, se := range s.sessions {
		if se.UserID == userID {
			out = append(out, *se)
		}
	}
	slices.SortFunc(out, func(a, b Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Memory) RenameSession(_ context.Context, userID, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.session(userID, id)
	if err != nil {
		return err
	}
	se.Title, se.UpdatedAt = title, s.now()
	return nil
}

func (s *Memory) DeleteSession(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.session(userID, id); err != nil {
		return err
	}
	delete(s.sessions, id)
	for mid, r := range s.messages {
		if r.SessionID == id {
			delete(s.messages, mid)
		}
	}
	return nil
}

func (s *Memory) AppendMessage(_ context.Context, userID, sessionID, parentID string, m llm.Message) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.session(userID, sessionID)
	if err != nil {
		return Record{}, err
	}
	if parentID != "" {
		if p, ok := s.messages[parentID]; !ok || p.SessionID != sessionID {
			return Record{}, ErrNotFound
		}
	}
	now := s.now()
	r := &Record{ID: NewID(), SessionID: sessionID, ParentID: parentID, Message: m, CreatedAt: now}
	s.messages[r.ID] = r
	se.HeadID, se.UpdatedAt = r.ID, now
	return *r, nil
}

func (s *Memory) Path(_ context.Context, userID, sessionID string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.session(userID, sessionID)
	if err != nil {
		return nil, err
	}
	var out []Record
	for id := se.HeadID; id != ""; {
		r := s.messages[id]
		out = append(out, *r)
		id = r.ParentID
	}
	slices.Reverse(out)
	return out, nil
}

func (s *Memory) SetHead(_ context.Context, userID, sessionID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.session(userID, sessionID)
	if err != nil {
		return err
	}
	if messageID != "" {
		if r, ok := s.messages[messageID]; !ok || r.SessionID != sessionID {
			return ErrNotFound
		}
	}
	se.HeadID, se.UpdatedAt = messageID, s.now()
	return nil
}

func (s *Memory) Close() error { return nil }
