package agent

import (
	"context"
	"encoding/json"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ToolResult 工具执行结果。
type ToolResult struct {
	Content   string // 给模型看的文本
	Details   any    // 给前端/日志看的结构化数据（模型看不到）
	Terminate bool   // 提示：这批工具执行完后不必再问模型
}

// UpdateFunc 工具执行过程中推送进度（例如长任务的中间输出）。
type UpdateFunc func(partial ToolResult)

// Tool 一个可被 Agent 调用的工具。
//
// Execute 返回 error 时，循环会把错误以 IsError=true 的 toolResult 交给模型，
// 而不是中断整个 Agent —— 工具不要自己把错误拼进 Content。
type Tool struct {
	Name        string
	Label       string         // 给人看的名字
	Description string         // 给模型看的说明，写得好坏直接影响调用准确率
	Parameters  map[string]any // JSON Schema

	// Sequential 为 true 时，同一批里只要有这个工具，整批改为顺序执行
	// （例如会修改文件、有先后依赖的工具）。
	Sequential bool

	Execute func(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate UpdateFunc) (ToolResult, error)
}

// Def 转成模型可见的工具定义。
func (t Tool) Def() llm.ToolDef {
	return llm.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
}
