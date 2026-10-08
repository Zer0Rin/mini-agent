package server

import (
	"time"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/store"
)

// 本文件定义 HTTP 接口的 JSON 形态（前端看到的格式），与内部结构解耦。
// 修改这里等于修改 API 契约，需要同步更新 docs/API.md 和前端类型。

type wireSession struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	HeadID    string    `json:"head_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type wireToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"` // 原始 JSON 字符串（模型给的，不保证合法）
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type wireMessage struct {
	ID         string         `json:"id,omitempty"`        // 已保存的消息才有
	ParentID   string         `json:"parent_id,omitempty"` // 空表示根
	Role       string         `json:"role"`                // user / assistant / toolResult
	Content    string         `json:"content"`
	Reasoning  string         `json:"reasoning,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	Usage      *wireUsage     `json:"usage,omitempty"`
	CreatedAt  *time.Time     `json:"created_at,omitempty"`
}

type wireDelta struct {
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

type wireToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// wireEvent SSE 推送的一个事件。type 与 agent.EventType 一致，另外加 error、done 两种。
type wireEvent struct {
	Type       string          `json:"type"`
	Message    *wireMessage    `json:"message,omitempty"`
	Delta      *wireDelta      `json:"delta,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Args       string          `json:"args,omitempty"`
	Result     *wireToolResult `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
}

const (
	eventError = "error" // 运行出错（之后仍会发 done）
	eventDone  = "done"  // 流结束，前端可以关闭连接
)

func toWireSession(s store.Session) wireSession {
	return wireSession{ID: s.ID, Title: s.Title, HeadID: s.HeadID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func toWireMessage(m llm.Message) *wireMessage {
	w := &wireMessage{Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, ToolCallID: m.ToolCallID,
		ToolName: m.ToolName, IsError: m.IsError}
	for _, tc := range m.ToolCalls {
		w.ToolCalls = append(w.ToolCalls, wireToolCall{ID: tc.ID, Name: tc.Name, Args: tc.ArgsJSON})
	}
	if m.Usage != nil {
		w.Usage = &wireUsage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens}
	}
	return w
}

func toWireRecord(r store.Record) *wireMessage {
	w := toWireMessage(r.Message)
	w.ID, w.ParentID = r.ID, r.ParentID
	t := r.CreatedAt
	w.CreatedAt = &t
	return w
}

// toWireEvent 把 agent 事件转成推送格式。message_end 由调用方单独处理（要先落库拿到 ID）。
func toWireEvent(e agent.Event) wireEvent {
	w := wireEvent{Type: string(e.Type), ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: string(e.Args)}
	if e.Message != nil {
		w.Message = toWireMessage(*e.Message)
	}
	if e.Delta != nil {
		w.Delta = &wireDelta{Content: e.Delta.Content, Reasoning: e.Delta.Reasoning}
	}
	if e.Result != nil {
		w.Result = &wireToolResult{Content: e.Result.Content, IsError: e.IsError}
	}
	return w
}
