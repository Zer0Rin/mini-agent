package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ErrMaxTurns 超过最大轮数仍未结束。
var ErrMaxTurns = errors.New("agent: max turns exceeded")

const defaultMaxTurns = 20

// ToolExecution 一批工具调用的执行方式。
type ToolExecution string

const (
	ToolsParallel   ToolExecution = "parallel"   // 默认：并发执行，结果按模型给出的顺序回填
	ToolsSequential ToolExecution = "sequential" // 逐个执行
)

// Config 一次运行的配置。
type Config struct {
	Model         llm.Model // 如果同时实现了 llm.StreamModel，自动使用流式调用
	SystemPrompt  string
	Tools         []Tool
	MaxTurns      int           // 安全阀：最多问模型几次（<=0 时用默认值 20）
	ToolExecution ToolExecution // 空值等同 ToolsParallel

	// TransformContext 每次调用模型前对消息做变换（裁剪过长的历史、注入上下文等）。
	// 只影响发给模型的内容，不改变返回的 newMsgs。
	TransformContext func(ctx context.Context, msgs []llm.Message) ([]llm.Message, error)

	// BeforeToolCall 工具执行前的拦截钩子：返回 block=true 时不执行，reason 会作为错误交给模型。
	// 危险工具审批、按用户做权限控制，都放在这里。
	BeforeToolCall func(ctx context.Context, call llm.ToolCall) (block bool, reason string)

	// AfterToolCall 工具执行后、交给模型前，可以改写结果（脱敏、截断过长输出等）。
	AfterToolCall func(ctx context.Context, call llm.ToolCall, res ToolResult, isErr bool) ToolResult

	// GetSteeringMessages 每轮工具执行完后调用：返回的消息会插在下一次问模型之前
	// （用户在 Agent 干活时插话、纠正方向）。
	GetSteeringMessages func() []llm.Message

	// GetFollowUpMessages 模型给出最终答案、且没有插话时调用：返回非空则继续下一轮
	// （用户排队的后续问题）。
	GetFollowUpMessages func() []llm.Message

	OnEvent Listener
}

// Loop 执行 ReAct 循环：问模型 → 执行工具 → 回填结果 → 再问，直到模型不再调用工具。
//
// history 是已有的对话，prompts 是本次新加的消息（可以为空，表示从 history 末尾继续）。
// 返回本次运行新增的全部消息（不含 history），调用方自己决定怎么保存。
// 出错时也会返回出错前已经产生的消息。
func Loop(ctx context.Context, cfg Config, history []llm.Message, prompts ...llm.Message) (newMsgs []llm.Message, err error) {
	r := &run{cfg: cfg}
	r.emit(ctx, Event{Type: EventAgentStart})
	defer func() { r.emit(ctx, Event{Type: EventAgentEnd, NewMessages: r.newMsgs}) }()

	for _, p := range prompts {
		r.addMsg(ctx, p)
	}

	r.byName = make(map[string]Tool, len(cfg.Tools))
	for _, t := range cfg.Tools {
		r.byName[t.Name] = t // 只有这里登记过的工具才能被执行（天然的允许名单）
		r.defs = append(r.defs, t.Def())
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return r.newMsgs, err
		}
		r.emit(ctx, Event{Type: EventTurnStart})

		// ① 思考
		reply, err := r.callModel(ctx, history)
		if err != nil {
			return r.newMsgs, err
		}

		// ② 没有工具调用 → 本轮结束；有插话或排队问题就继续，否则整体结束
		if len(reply.ToolCalls) == 0 {
			r.emit(ctx, Event{Type: EventTurnEnd, Message: &reply})
			if r.pollQueue(ctx, cfg.GetSteeringMessages) || r.pollQueue(ctx, cfg.GetFollowUpMessages) {
				continue
			}
			return r.newMsgs, nil
		}

		// ③ 行动 + 观察
		results := r.executeTools(ctx, reply.ToolCalls)
		terminateAll := true
		for i, call := range reply.ToolCalls {
			res := results[i]
			r.addMsg(ctx, llm.Message{
				Role:       llm.RoleToolResult,
				Content:    res.result.Content,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				IsError:    res.isErr,
			})
			if res.isErr || !res.result.Terminate {
				terminateAll = false
			}
		}
		r.emit(ctx, Event{Type: EventTurnEnd, Message: &reply})

		steered := r.pollQueue(ctx, cfg.GetSteeringMessages)
		if terminateAll && !steered {
			return r.newMsgs, nil // 这批工具全部要求结束，且用户没有插话
		}
	}
	return r.newMsgs, ErrMaxTurns
}

// run 一次 Loop 的内部状态。
type run struct {
	cfg     Config
	byName  map[string]Tool
	defs    []llm.ToolDef
	newMsgs []llm.Message
	emitMu  sync.Mutex // 并行执行工具时，多个 goroutine 会同时发事件；监听器必须串行调用
}

func (r *run) emit(ctx context.Context, e Event) {
	if r.cfg.OnEvent == nil {
		return
	}
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.cfg.OnEvent(ctx, e)
}

func (r *run) addMsg(ctx context.Context, m llm.Message) {
	r.emit(ctx, Event{Type: EventMessageStart, Message: &m})
	r.emit(ctx, Event{Type: EventMessageEnd, Message: &m})
	r.newMsgs = append(r.newMsgs, m)
}

// pollQueue 取出队列里的消息并加入对话；返回是否取到了消息。
func (r *run) pollQueue(ctx context.Context, get func() []llm.Message) bool {
	if get == nil {
		return false
	}
	msgs := get()
	for _, m := range msgs {
		r.addMsg(ctx, m)
	}
	return len(msgs) > 0
}

// callModel 组装上下文并调用模型；支持流式时发出 message_update。
func (r *run) callModel(ctx context.Context, history []llm.Message) (llm.Message, error) {
	msgs := make([]llm.Message, 0, len(history)+len(r.newMsgs))
	msgs = append(append(msgs, history...), r.newMsgs...)
	if r.cfg.TransformContext != nil {
		var err error
		if msgs, err = r.cfg.TransformContext(ctx, msgs); err != nil {
			return llm.Message{}, fmt.Errorf("transform context: %w", err)
		}
	}
	in := llm.Context{SystemPrompt: r.cfg.SystemPrompt, Messages: msgs, Tools: r.defs}

	var reply llm.Message
	var err error
	if sm, ok := r.cfg.Model.(llm.StreamModel); ok {
		r.emit(ctx, Event{Type: EventMessageStart, Message: &llm.Message{Role: llm.RoleAssistant}})
		reply, err = sm.Stream(ctx, in, func(d llm.Delta) {
			r.emit(ctx, Event{Type: EventMessageUpdate, Delta: &d})
		})
		if err != nil {
			return llm.Message{}, fmt.Errorf("model: %w", err)
		}
		reply.Role = llm.RoleAssistant
		r.emit(ctx, Event{Type: EventMessageEnd, Message: &reply})
		r.newMsgs = append(r.newMsgs, reply)
		return reply, nil
	}

	if reply, err = r.cfg.Model.Complete(ctx, in); err != nil {
		return llm.Message{}, fmt.Errorf("model: %w", err)
	}
	reply.Role = llm.RoleAssistant
	r.addMsg(ctx, reply)
	return reply, nil
}

type toolOutcome struct {
	result ToolResult
	isErr  bool
}

// executeTools 执行一批工具调用，返回结果的顺序与 calls 一致。
//
// 并行模式：查表和 BeforeToolCall 按顺序做完（钩子可能要弹审批，不能乱序），
// 通过检查的工具再并发执行；tool_execution_end 按完成顺序发出，
// 但回填给模型的结果始终按模型给出的顺序。
func (r *run) executeTools(ctx context.Context, calls []llm.ToolCall) []toolOutcome {
	out := make([]toolOutcome, len(calls))
	pending := make([]int, 0, len(calls)) // 通过预检、需要真正执行的调用下标

	parallel := r.cfg.ToolExecution != ToolsSequential
	for i, call := range calls {
		r.emit(ctx, Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Args: []byte(call.ArgsJSON)})
		t, ok := r.byName[call.Name]
		switch {
		case !ok:
			out[i] = r.finishTool(ctx, call, ToolResult{Content: fmt.Sprintf("tool %q not found", call.Name)}, true)
		case r.cfg.BeforeToolCall != nil:
			if block, reason := r.cfg.BeforeToolCall(ctx, call); block {
				out[i] = r.finishTool(ctx, call, ToolResult{Content: "tool call blocked: " + reason}, true)
				continue
			}
			pending = append(pending, i)
		default:
			pending = append(pending, i)
		}
		if ok && t.Sequential {
			parallel = false
		}
	}

	exec := func(i int) {
		call := calls[i]
		res, err := r.safeExecute(ctx, r.byName[call.Name], call)
		if err != nil {
			out[i] = r.finishTool(ctx, call, ToolResult{Content: err.Error()}, true)
			return
		}
		out[i] = r.finishTool(ctx, call, res, false)
	}

	if !parallel || len(pending) <= 1 {
		for _, i := range pending {
			exec(i)
		}
		return out
	}
	var wg sync.WaitGroup
	for _, i := range pending {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exec(i) // 每个 goroutine 只写 out[i]，互不冲突
		}(i)
	}
	wg.Wait()
	return out
}

// finishTool 走 AfterToolCall 钩子并发出 tool_execution_end。
func (r *run) finishTool(ctx context.Context, call llm.ToolCall, res ToolResult, isErr bool) toolOutcome {
	if r.cfg.AfterToolCall != nil {
		res = r.cfg.AfterToolCall(ctx, call, res, isErr)
	}
	r.emit(ctx, Event{Type: EventToolExecutionEnd, ToolCallID: call.ID, ToolName: call.Name, Result: &res, IsError: isErr})
	return toolOutcome{result: res, isErr: isErr}
}

// safeExecute 执行工具并兜底 panic。
// 注意返回值必须命名，defer 里的 recover 才能改写它（nexus-agent 第四章的坑）。
func (r *run) safeExecute(ctx context.Context, t Tool, call llm.ToolCall) (res ToolResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool %s panicked: %v", t.Name, p)
		}
	}()
	args := call.ArgsJSON
	if args == "" {
		args = "{}" // 有的模型无参调用时给空字符串
	}
	onUpdate := func(partial ToolResult) {
		r.emit(ctx, Event{Type: EventToolExecutionUpdate, ToolCallID: call.ID, ToolName: call.Name, Result: &partial})
	}
	return t.Execute(ctx, call.ID, []byte(args), onUpdate)
}
