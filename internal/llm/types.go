// Package llm 定义与大模型交互的消息、工具定义和模型接口。
// 设计参考 pi-ai：消息只有 user / assistant / toolResult 三种角色，
// system prompt 不进消息列表，单独传给模型。
package llm

import "context"

// 消息角色。
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
)

// Message 一条对话消息。
type Message struct {
	Role      string
	Content   string
	Reasoning string // 推理模型的思考过程（只用于展示，不回传给模型）

	// Role = assistant：模型请求调用的工具（可能一次多个）
	ToolCalls []ToolCall

	// Role = toolResult：这条结果对应哪一次调用
	ToolCallID string
	ToolName   string
	IsError    bool // 工具执行失败，Content 里是错误描述

	// Role = assistant：本次调用的 token 用量（上游没返回时为 nil）
	Usage *Usage
}

// Usage token 用量。
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ToolCall 模型发起的一次工具调用。
type ToolCall struct {
	ID       string
	Name     string
	ArgsJSON string
}

// ToolDef 告诉模型"有哪些工具可用"。
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// Context 一次模型调用的完整输入。
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDef
}

// Model 大模型的最小抽象：Agent 只依赖这个接口。
// 返回一条 assistant 消息。真实实现调用 DeepSeek，测试时换成假模型。
type Model interface {
	Complete(ctx context.Context, c Context) (Message, error)
}

// Delta 流式输出的一个增量片段。
type Delta struct {
	Content   string
	Reasoning string
}

// StreamModel 支持流式输出的模型（可选能力）。
// 循环发现模型实现了这个接口，就改用流式调用，并发出 message_update 事件。
// onDelta 在同一个 goroutine 里按顺序调用；返回的 Message 是拼好的完整消息。
type StreamModel interface {
	Model
	Stream(ctx context.Context, c Context, onDelta func(Delta)) (Message, error)
}
