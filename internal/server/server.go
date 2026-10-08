// Package server HTTP + SSE 接口：会话管理、发送消息（流式推送 Agent 事件）、中断、插话。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/store"
)

// Options 服务配置。
type Options struct {
	Store store.Store
	// AgentConfig 每次运行时调用，返回模型、工具、system prompt 等（OnEvent 由服务接管）。
	AgentConfig func() agent.Config
	// Token 非空时，所有 /api 请求必须带 Authorization: Bearer <Token>。
	Token  string
	Logger *slog.Logger
}

// Server HTTP 服务。
type Server struct {
	opts    Options
	log     *slog.Logger
	mu      sync.Mutex
	running map[string]*runSlot // sessionID → 正在运行的会话（用于防重入、中断、插话）
}

// runSlot 一次运行占用的位置。先占位、再加载历史，避免两个请求读到同一个旧 head。
type runSlot struct {
	agent *agent.Agent // 加载历史期间为 nil
}

// New 创建服务。
func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{opts: opts, log: log, running: map[string]*runSlot{}}
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	api := http.NewServeMux()
	api.HandleFunc("GET /api/sessions", s.listSessions)
	api.HandleFunc("POST /api/sessions", s.createSession)
	api.HandleFunc("GET /api/sessions/{id}", s.getSession)
	api.HandleFunc("PATCH /api/sessions/{id}", s.renameSession)
	api.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	api.HandleFunc("POST /api/sessions/{id}/head", s.setHead)
	api.HandleFunc("POST /api/sessions/{id}/messages", s.sendMessage)
	api.HandleFunc("POST /api/sessions/{id}/abort", s.abort)
	api.HandleFunc("POST /api/sessions/{id}/steer", s.steer)
	api.HandleFunc("POST /api/sessions/{id}/follow-up", s.followUp)
	mux.Handle("/api/", s.auth(api))
	return mux
}

// userID 当前请求的用户。目前是单用户；接入登录后只需改这一个函数，
// Store 的每个方法都已经按用户过滤。
func userID(*http.Request) string { return "local" }

// auth 可选的 Bearer Token 校验（nexus-agent A2A 端点无鉴权的教训）。
func (s *Server) auth(next http.Handler) http.Handler {
	if s.opts.Token == "" {
		return next
	}
	want := "Bearer " + s.opts.Token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- 会话 ----

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Store.ListSessions(r.Context(), userID(r), 100)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]wireSession, 0, len(list))
	for _, se := range list {
		out = append(out, toWireSession(se))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	se, err := s.opts.Store.CreateSession(r.Context(), userID(r), strings.TrimSpace(body.Title))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWireSession(se))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	se, err := s.opts.Store.GetSession(r.Context(), userID(r), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	path, err := s.opts.Store.Path(r.Context(), userID(r), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	msgs := make([]*wireMessage, 0, len(path))
	for _, rec := range path {
		msgs = append(msgs, toWireRecord(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  toWireSession(se),
		"messages": msgs,
		"running":  s.isBusy(id),
	})
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.opts.Store.RenameSession(r.Context(), userID(r), r.PathValue("id"), strings.TrimSpace(body.Title)); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.isBusy(id) {
		writeError(w, http.StatusConflict, "session is running")
		return
	}
	if err := s.opts.Store.DeleteSession(r.Context(), userID(r), id); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setHead 分叉：把会话的当前位置移到某条历史消息，之后发送的消息会长出新分支。
func (s *Server) setHead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if s.isBusy(id) {
		writeError(w, http.StatusConflict, "session is running")
		return
	}
	if err := s.opts.Store.SetHead(r.Context(), userID(r), id, body.MessageID); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- 运行 ----

// sendMessage 追加一条用户消息并运行 Agent，用 SSE 推送全部事件，直到 done。
// 每条消息在 message_end 时落库（拿到 ID 后再推给前端），所以中途断开也不会丢已完成的消息。
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	uid, sid := userID(r), r.PathValue("id")
	if _, err := s.opts.Store.GetSession(r.Context(), uid, sid); err != nil {
		s.storeError(w, err) // 先确认会话属于当前用户，再占位
		return
	}
	slot := &runSlot{}
	if !s.claim(sid, slot) {
		writeError(w, http.StatusConflict, "session is already running")
		return
	}
	defer s.release(sid)

	// 占位之后再读会话和历史：保证拿到的 head 是最新的
	se, err := s.opts.Store.GetSession(r.Context(), uid, sid)
	if err != nil {
		s.storeError(w, err)
		return
	}
	path, err := s.opts.Store.Path(r.Context(), uid, sid)
	if err != nil {
		s.storeError(w, err)
		return
	}
	history := make([]llm.Message, 0, len(path))
	for _, rec := range path {
		history = append(history, rec.Message)
	}
	ag := agent.New(s.opts.AgentConfig(), history)
	s.mu.Lock()
	slot.agent = ag
	s.mu.Unlock()

	if se.Title == "" { // 第一条消息自动作为标题
		_ = s.opts.Store.RenameSession(r.Context(), uid, sid, truncate(content, 30))
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // 让 Nginx 等反向代理不要缓冲
	w.WriteHeader(http.StatusOK)

	send := func(e wireEvent) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
		flusher.Flush()
	}

	// 落库不跟随请求取消：用户关掉页面时，已经完成的消息仍然要保存
	persistCtx := context.WithoutCancel(r.Context())
	parent := se.HeadID
	ag.Subscribe(func(_ context.Context, e agent.Event) {
		if e.Type != agent.EventMessageEnd {
			send(toWireEvent(e))
			return
		}
		rec, err := s.opts.Store.AppendMessage(persistCtx, uid, sid, parent, *e.Message)
		if err != nil {
			s.log.Error("persist message", "session", sid, "err", err)
			send(wireEvent{Type: eventError, Error: "failed to save message"})
			return
		}
		parent = rec.ID
		send(wireEvent{Type: string(agent.EventMessageEnd), Message: toWireRecord(rec)})
	})

	err = ag.Prompt(r.Context(), llm.Message{Role: llm.RoleUser, Content: content})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("agent run failed", "session", sid, "err", err)
		}
		send(wireEvent{Type: eventError, Error: errorMessage(err)})
	}
	send(wireEvent{Type: eventDone})
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	ag := s.ownedAgent(w, r)
	if ag == nil {
		return
	}
	ag.Abort()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	s.enqueue(w, r, func(ag *agent.Agent, m llm.Message) { ag.Steer(m) })
}

func (s *Server) followUp(w http.ResponseWriter, r *http.Request) {
	s.enqueue(w, r, func(ag *agent.Agent, m llm.Message) { ag.FollowUp(m) })
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, put func(*agent.Agent, llm.Message)) {
	var body struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	ag := s.ownedAgent(w, r)
	if ag == nil {
		return
	}
	put(ag, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(body.Content)})
	w.WriteHeader(http.StatusAccepted)
}

// ownedAgent 取出正在运行的 Agent，并确认会话属于当前用户。
func (s *Server) ownedAgent(w http.ResponseWriter, r *http.Request) *agent.Agent {
	sid := r.PathValue("id")
	if _, err := s.opts.Store.GetSession(r.Context(), userID(r), sid); err != nil {
		s.storeError(w, err)
		return nil
	}
	ag := s.agentFor(sid)
	if ag == nil {
		writeError(w, http.StatusConflict, "session is not running")
	}
	return ag
}

func (s *Server) claim(sid string, slot *runSlot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.running[sid]; busy {
		return false
	}
	s.running[sid] = slot
	return true
}

func (s *Server) isBusy(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, busy := s.running[sid]
	return busy
}

func (s *Server) release(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, sid)
}

// agentFor 正在运行的 Agent；没在运行或还在加载历史时返回 nil。
func (s *Server) agentFor(sid string) *agent.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slot := s.running[sid]; slot != nil {
		return slot.agent
	}
	return nil
}

// ---- 工具函数 ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 请求体最大 1MB
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.internalError(w, err)
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error") // 不把内部错误细节返回给前端
}

// errorMessage 给前端看的错误说明。
func errorMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "aborted"
	case errors.Is(err, agent.ErrMaxTurns):
		return "max turns exceeded"
	default:
		var apiErr *llm.APIError
		if errors.As(err, &apiErr) {
			return fmt.Sprintf("model api error (status %d)", apiErr.Status)
		}
		return "agent error"
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
