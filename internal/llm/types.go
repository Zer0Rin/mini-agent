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
	Role    string
	Content string

	// Role = assistant：模型请求调用的工具（可能一次多个）
	ToolCalls []ToolCall

	// Role = toolResult：这条结果对应哪一次调用
	ToolCallID string
	ToolName   string
	IsError    bool // 工具执行失败，Content 里是错误描述
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
