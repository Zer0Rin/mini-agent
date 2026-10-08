package agent

import (
	"context"
	"encoding/json"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// EventType 事件类型（与 pi-agent-core 同名）。
type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageEnd          EventType = "message_end"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
)

// Event 循环对外发出的事件。不同类型只填相关字段。
type Event struct {
	Type EventType

	// message_start / message_end
	Message *llm.Message

	// tool_execution_*
	ToolCallID string
	ToolName   string
	Args       json.RawMessage
	Result     *ToolResult
	IsError    bool

	// agent_end：本次运行新增的全部消息
	NewMessages []llm.Message
}

// Listener 事件监听器。
//
// 同步调用、按顺序执行：监听器处理完一个事件，循环才继续往下走。
// 所以事件不会丢，也不会乱序（对比 nexus-agent 里满了就丢的 emit）。
type Listener func(ctx context.Context, e Event)
