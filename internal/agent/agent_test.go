package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// blockingModel 一直等到 ctx 取消或收到放行信号。
type blockingModel struct {
	started chan struct{}
	release chan llm.Message
}

func newBlockingModel() *blockingModel {
	return &blockingModel{started: make(chan struct{}, 10), release: make(chan llm.Message, 10)}
}

func (m *blockingModel) Complete(ctx context.Context, _ llm.Context) (llm.Message, error) {
	m.started <- struct{}{}
	select {
	case msg := <-m.release:
		return msg, nil
	case <-ctx.Done():
		return llm.Message{}, ctx.Err()
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("等待超时")
	}
}

func TestAgent_KeepsHistoryAcrossPrompts(t *testing.T) {
	m := &scriptedModel{replies: []llm.Message{{Content: "答1"}, {Content: "答2"}}}
	a := New(Config{Model: m}, nil)
	ctx := context.Background()
	if err := a.Prompt(ctx, userMsg("问1")); err != nil {
		t.Fatal(err)
	}
	if err := a.Prompt(ctx, userMsg("问2")); err != nil {
		t.Fatal(err)
	}
	if got := len(m.calls[1].Messages); got != 3 { // 问1、答1、问2
		t.Errorf("第二次运行应带上历史，模型收到 %d 条", got)
	}
	if got := len(a.Messages()); got != 4 {
		t.Errorf("Messages() = %d 条，期望 4", got)
	}
}

func TestAgent_BusyAndAbort(t *testing.T) {
	m := newBlockingModel()
	a := New(Config{Model: m}, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- a.Prompt(context.Background(), userMsg("慢慢想")) }()
	waitSignal(t, m.started)

	if err := a.Prompt(context.Background(), userMsg("插队")); !errors.Is(err, ErrBusy) {
		t.Errorf("运行中再次 Prompt 应返回 ErrBusy，实际 %v", err)
	}
	if !a.IsRunning() {
		t.Error("IsRunning 应为 true")
	}

	a.Abort()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("中断后应返回 context.Canceled，实际 %v", err)
	}
	if err := a.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 被中断时，已经加入的用户消息要保留，方便之后 Continue
	msgs := a.Messages()
	if len(msgs) != 1 || msgs[0].Content != "慢慢想" {
		t.Errorf("中断后应保留用户消息，实际 %+v", msgs)
	}

	// Continue：最后一条是 user，可以继续
	m.release <- llm.Message{Content: "想好了"}
	if err := a.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Continue(context.Background()); !errors.Is(err, ErrCannotContinue) {
		t.Errorf("最后一条是 assistant 时 Continue 应失败，实际 %v", err)
	}
}

// 工具执行期间用户插话：插话应在工具结束后、下一次问模型前送达
func TestAgent_SteerWhileToolRunning(t *testing.T) {
	toolStarted := make(chan struct{})
	toolRelease := make(chan struct{})
	slow := Tool{Name: "slow", Execute: func(ctx context.Context, _ string, _ json.RawMessage, _ UpdateFunc) (ToolResult, error) {
		close(toolStarted)
		<-toolRelease
		return ToolResult{Content: "done"}, nil
	}}
	m := &scriptedModel{replies: []llm.Message{callTool("c1", "slow", `{}`), {Content: "收到插话"}}}
	a := New(Config{Model: m, Tools: []Tool{slow}}, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- a.Prompt(context.Background(), userMsg("开始")) }()
	waitSignal(t, toolStarted)
	a.Steer(userMsg("顺便用中文"))
	close(toolRelease)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	msgs := m.calls[1].Messages
	if last := msgs[len(msgs)-1]; last.Content != "顺便用中文" {
		t.Errorf("插话没有在下一次问模型前送达，最后一条: %+v", last)
	}
}

func TestAgent_SubscribeOrderAndUnsubscribe(t *testing.T) {
	m := &scriptedModel{replies: []llm.Message{{Content: "a"}, {Content: "b"}}}
	a := New(Config{Model: m}, nil)

	var order []string
	a.Subscribe(func(_ context.Context, e Event) {
		if e.Type == EventAgentEnd {
			order = append(order, "first")
		}
	})
	unsub := a.Subscribe(func(_ context.Context, e Event) {
		if e.Type == EventAgentEnd {
			order = append(order, "second")
		}
	})
	_ = a.Prompt(context.Background(), userMsg("1"))
	unsub()
	_ = a.Prompt(context.Background(), userMsg("2"))

	want := []string{"first", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("调用记录 = %v，期望 %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("调用记录 = %v，期望 %v", order, want)
		}
	}
}

// 监听器里调用 Agent 自己的方法不能死锁
func TestAgent_ListenerCanCallAgent(t *testing.T) {
	m := &scriptedModel{replies: []llm.Message{{Content: "a"}, {Content: "b"}}}
	a := New(Config{Model: m}, nil)
	a.Subscribe(func(_ context.Context, e Event) {
		if e.Type == EventTurnEnd && len(m.calls) == 1 {
			a.FollowUp(userMsg("再来一个"))
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), userMsg("1")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("死锁了")
	}
	if len(m.calls) != 2 {
		t.Errorf("监听器里排队的问题应被处理，模型调用 %d 次", len(m.calls))
	}
}
