package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ErrMaxTurns 超过最大轮数仍未结束。
var ErrMaxTurns = errors.New("agent: max turns exceeded")

const defaultMaxTurns = 20

// Config 一次运行的配置。
type Config struct {
	Model        llm.Model
	SystemPrompt string
	Tools        []Tool
	MaxTurns     int // 安全阀：最多问模型几次（<=0 时用默认值）

	// BeforeToolCall 工具执行前的拦截钩子：返回 block=true 时不执行，reason 会作为错误交给模型。
	// 危险工具审批、按用户做权限控制，都放在这里。
	BeforeToolCall func(ctx context.Context, call llm.ToolCall) (block bool, reason string)

	OnEvent Listener
}

// Loop 执行 ReAct 循环：问模型 → 执行工具 → 回填结果 → 再问，直到模型不再调用工具。
//
// history 是已有的对话，prompts 是本次新加的用户消息。
// 返回本次运行新增的全部消息（不含 history），调用方自己决定怎么保存。
func Loop(ctx context.Context, cfg Config, history []llm.Message, prompts ...llm.Message) (newMsgs []llm.Message, err error) {
	emit := func(e Event) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(ctx, e)
		}
	}
	addMsg := func(m llm.Message) {
		emit(Event{Type: EventMessageStart, Message: &m})
		emit(Event{Type: EventMessageEnd, Message: &m})
		newMsgs = append(newMsgs, m)
	}

	emit(Event{Type: EventAgentStart})
	defer func() { emit(Event{Type: EventAgentEnd, NewMessages: newMsgs}) }()

	for _, p := range prompts {
		addMsg(p)
	}

	// 工具表：只有这里登记过的工具才能被执行（天然的允许名单）
	byName := make(map[string]Tool, len(cfg.Tools))
	defs := make([]llm.ToolDef, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		byName[t.Name] = t
		defs = append(defs, t.Def())
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return newMsgs, err
		}
		emit(Event{Type: EventTurnStart})

		// ① 思考：每次都把完整对话发给模型
		all := make([]llm.Message, 0, len(history)+len(newMsgs))
		all = append(append(all, history...), newMsgs...)
		reply, err := cfg.Model.Complete(ctx, llm.Context{
			SystemPrompt: cfg.SystemPrompt,
			Messages:     all,
			Tools:        defs,
		})
		if err != nil {
			return newMsgs, fmt.Errorf("model: %w", err)
		}
		reply.Role = llm.RoleAssistant
		addMsg(reply)

		// ② 没有工具调用 → 模型给出了最终答案
		if len(reply.ToolCalls) == 0 {
			emit(Event{Type: EventTurnEnd, Message: &reply})
			return newMsgs, nil
		}

		// ③ 行动 + 观察：按顺序执行每个工具，结果回填
		terminateAll := true
		for _, call := range reply.ToolCalls {
			res, isErr := runToolCall(ctx, cfg, byName, call, emit)
			addMsg(llm.Message{
				Role:       llm.RoleToolResult,
				Content:    res.Content,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				IsError:    isErr,
			})
			if isErr || !res.Terminate {
				terminateAll = false
			}
		}
		emit(Event{Type: EventTurnEnd, Message: &reply})

		// 这批工具全部要求结束 → 不再问模型
		if terminateAll {
			return newMsgs, nil
		}
	}
	return newMsgs, ErrMaxTurns
}

// runToolCall 执行一次工具调用。任何失败都变成 isErr=true 的结果，不会中断循环。
func runToolCall(ctx context.Context, cfg Config, byName map[string]Tool, call llm.ToolCall, emit func(Event)) (res ToolResult, isErr bool) {
	emit(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Args: []byte(call.ArgsJSON)})
	defer func() {
		emit(Event{Type: EventToolExecutionEnd, ToolCallID: call.ID, ToolName: call.Name, Result: &res, IsError: isErr})
	}()

	t, ok := byName[call.Name]
	if !ok {
		return ToolResult{Content: fmt.Sprintf("tool %q not found", call.Name)}, true
	}
	if cfg.BeforeToolCall != nil {
		if block, reason := cfg.BeforeToolCall(ctx, call); block {
			return ToolResult{Content: "tool call blocked: " + reason}, true
		}
	}

	onUpdate := func(partial ToolResult) {
		emit(Event{Type: EventToolExecutionUpdate, ToolCallID: call.ID, ToolName: call.Name, Result: &partial})
	}
	out, err := safeExecute(ctx, t, call, onUpdate)
	if err != nil {
		return ToolResult{Content: err.Error()}, true
	}
	return out, false
}

// safeExecute 执行工具并兜底 panic。
// 注意返回值必须命名，defer 里的 recover 才能改写它（nexus-agent 第四章的坑）。
func safeExecute(ctx context.Context, t Tool, call llm.ToolCall, onUpdate UpdateFunc) (res ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v", t.Name, r)
		}
	}()
	args := call.ArgsJSON
	if args == "" {
		args = "{}" // 有的模型无参调用时给空字符串
	}
	return t.Execute(ctx, call.ID, []byte(args), onUpdate)
}
