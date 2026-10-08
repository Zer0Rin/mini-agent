package agent

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

var (
	// ErrBusy 上一次运行还没结束。
	ErrBusy = errors.New("agent: already running")
	// ErrCannotContinue Continue 要求最后一条消息是 user 或 toolResult。
	ErrCannotContinue = errors.New("agent: last message must be user or toolResult to continue")
)

// Agent 有状态的外壳：保存对话、管理订阅者、插话/排队队列和中断。
// Loop 是无状态的纯循环；需要"边跑边插话、随时中断"时用 Agent。
//
// 同一时刻只允许一次运行；所有方法都可以被多个 goroutine 并发调用。
type Agent struct {
	base Config // Model、SystemPrompt、Tools、钩子等；OnEvent 和两个队列函数由 Agent 接管

	mu        sync.Mutex
	messages  []llm.Message
	listeners []listenerEntry
	nextID    int
	steerQ    []llm.Message
	followQ   []llm.Message

	running bool
	cancel  context.CancelFunc
	idle    chan struct{} // 空闲时是已关闭的 channel；运行中是未关闭的
	lastErr error
}

type listenerEntry struct {
	id int
	fn Listener
}

// New 创建 Agent。history 是已有的对话（例如从数据库恢复的会话）。
func New(cfg Config, history []llm.Message) *Agent {
	idle := make(chan struct{})
	close(idle)
	return &Agent{base: cfg, messages: slices.Clone(history), idle: idle}
}

// Subscribe 订阅事件，返回取消订阅的函数。监听器按订阅顺序同步调用。
// 注意：agent_end 发出时 Messages() 还没更新，请使用事件里的 NewMessages。
func (a *Agent) Subscribe(l Listener) (unsubscribe func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	id := a.nextID
	a.listeners = append(a.listeners, listenerEntry{id: id, fn: l})
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.listeners = slices.DeleteFunc(a.listeners, func(e listenerEntry) bool { return e.id == id })
	}
}

// Prompt 追加消息并运行，直到 Agent 停下。阻塞调用；想异步就放进 goroutine。
func (a *Agent) Prompt(ctx context.Context, msgs ...llm.Message) error {
	return a.run(ctx, msgs, false)
}

// Continue 不追加新消息，从现有对话末尾继续（例如上次出错或被中断后重试）。
func (a *Agent) Continue(ctx context.Context) error {
	return a.run(ctx, nil, true)
}

// Steer 插话：在当前这批工具执行完后、下一次问模型之前插入。
// 空闲时调用，消息会留在队列里，下一次运行时生效。
func (a *Agent) Steer(m llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steerQ = append(a.steerQ, m)
}

// FollowUp 排队：等模型给出最终答案后再处理。
func (a *Agent) FollowUp(m llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followQ = append(a.followQ, m)
}

// ClearQueues 清空插话和排队队列。
func (a *Agent) ClearQueues() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steerQ, a.followQ = nil, nil
}

// Abort 中断当前运行（没在运行时什么也不做）。正在运行的 Prompt 会返回 context.Canceled。
func (a *Agent) Abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// WaitForIdle 等待当前运行结束。
func (a *Agent) WaitForIdle(ctx context.Context) error {
	a.mu.Lock()
	idle := a.idle
	a.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Messages 当前对话的副本。
func (a *Agent) Messages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.messages)
}

// IsRunning 是否正在运行。
func (a *Agent) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// LastError 上一次运行的错误（成功时为 nil）。
func (a *Agent) LastError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// Update 修改配置（换模型、换工具、改 system prompt），下一次运行生效。运行中返回 ErrBusy。
func (a *Agent) Update(fn func(c *Config)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ErrBusy
	}
	fn(&a.base)
	return nil
}

// Reset 清空对话和队列。运行中返回 ErrBusy。
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ErrBusy
	}
	a.messages, a.steerQ, a.followQ, a.lastErr = nil, nil, nil, nil
	return nil
}

func (a *Agent) run(ctx context.Context, prompts []llm.Message, isContinue bool) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return ErrBusy
	}
	if isContinue {
		if n := len(a.messages); n == 0 || (a.messages[n-1].Role != llm.RoleUser && a.messages[n-1].Role != llm.RoleToolResult) {
			a.mu.Unlock()
			return ErrCannotContinue
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.running, a.cancel, a.idle = true, cancel, make(chan struct{})
	history := slices.Clone(a.messages)
	cfg := a.base
	a.mu.Unlock()

	cfg.OnEvent = a.dispatch
	cfg.GetSteeringMessages = func() []llm.Message { return a.drain(&a.steerQ) }
	cfg.GetFollowUpMessages = func() []llm.Message { return a.drain(&a.followQ) }

	newMsgs, err := Loop(runCtx, cfg, history, prompts...)
	cancel()

	a.mu.Lock()
	a.messages = append(a.messages, newMsgs...) // 出错或被中断时，也保留已经产生的消息
	a.running, a.cancel, a.lastErr = false, nil, err
	close(a.idle)
	a.mu.Unlock()
	return err
}

// dispatch 把事件依次交给所有监听器。先在锁内复制一份名单，再在锁外调用，
// 这样监听器里可以安全地调用 Steer、Abort 等方法而不会死锁。
func (a *Agent) dispatch(ctx context.Context, e Event) {
	a.mu.Lock()
	ls := slices.Clone(a.listeners)
	a.mu.Unlock()
	for _, l := range ls {
		l.fn(ctx, e)
	}
}

func (a *Agent) drain(q *[]llm.Message) []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	msgs := *q
	*q = nil
	return msgs
}
