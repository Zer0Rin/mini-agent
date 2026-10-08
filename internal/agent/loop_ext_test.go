package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ---- 流式 ----

// streamingModel 按剧本流式输出：先逐段推送内容，再返回完整消息。
type streamingModel struct{ pieces []string }

func (m *streamingModel) Complete(ctx context.Context, c llm.Context) (llm.Message, error) {
	return m.Stream(ctx, c, nil)
}

func (m *streamingModel) Stream(_ context.Context, _ llm.Context, onDelta func(llm.Delta)) (llm.Message, error) {
	for _, p := range m.pieces {
		if onDelta != nil {
			onDelta(llm.Delta{Content: p})
		}
	}
	return llm.Message{Content: strings.Join(m.pieces, "")}, nil
}

func TestLoop_StreamingEmitsUpdates(t *testing.T) {
	var types []EventType
	var deltas []string
	cfg := Config{
		Model: &streamingModel{pieces: []string{"你", "好", "！"}},
		OnEvent: func(_ context.Context, e Event) {
			types = append(types, e.Type)
			if e.Type == EventMessageUpdate {
				deltas = append(deltas, e.Delta.Content)
			}
		},
	}
	got, err := Loop(context.Background(), cfg, nil, userMsg("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "你好！" {
		t.Errorf("增量 = %v", deltas)
	}
	if got[len(got)-1].Content != "你好！" || got[len(got)-1].Role != llm.RoleAssistant {
		t.Errorf("最终消息不对: %+v", got[len(got)-1])
	}
	// assistant 消息：start → update×3 → end
	want := []EventType{EventMessageStart, EventMessageUpdate, EventMessageUpdate, EventMessageUpdate, EventMessageEnd}
	start := 4 // agent_start, user start/end, turn_start 之后
	for i, w := range want {
		if types[start+i] != w {
			t.Fatalf("第 %d 个事件 = %s，期望 %s（全部: %v）", start+i, types[start+i], w, types)
		}
	}
}

// ---- 并行执行工具 ----

func sleepTool(name string, d time.Duration, sequential bool) Tool {
	return Tool{
		Name:       name,
		Sequential: sequential,
		Execute: func(ctx context.Context, _ string, _ json.RawMessage, _ UpdateFunc) (ToolResult, error) {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
			return ToolResult{Content: name + " done"}, nil
		},
	}
}

func twoCalls(a, b string) llm.Message {
	return llm.Message{ToolCalls: []llm.ToolCall{{ID: "1", Name: a, ArgsJSON: "{}"}, {ID: "2", Name: b, ArgsJSON: "{}"}}}
}

func TestLoop_ParallelTools(t *testing.T) {
	cases := []struct {
		name        string
		exec        ToolExecution
		slowSeq     bool
		wantMinTime time.Duration
		wantMaxTime time.Duration
	}{
		{"默认并行", "", false, 0, 320 * time.Millisecond},
		{"配置为顺序", ToolsSequential, false, 340 * time.Millisecond, 2 * time.Second},
		{"有一个工具要求顺序", "", true, 340 * time.Millisecond, 2 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &scriptedModel{replies: []llm.Message{twoCalls("slow", "fast"), {Content: "ok"}}}
			var mu sync.Mutex
			var endOrder []string
			cfg := Config{
				Model:         m,
				ToolExecution: c.exec,
				Tools:         []Tool{sleepTool("slow", 200*time.Millisecond, c.slowSeq), sleepTool("fast", 150*time.Millisecond, false)},
				OnEvent: func(_ context.Context, e Event) {
					if e.Type == EventToolExecutionEnd {
						mu.Lock()
						endOrder = append(endOrder, e.ToolName)
						mu.Unlock()
					}
				},
			}
			start := time.Now()
			if _, err := Loop(context.Background(), cfg, nil, userMsg("go")); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			if elapsed < c.wantMinTime || elapsed > c.wantMaxTime {
				t.Errorf("耗时 %v，期望在 [%v, %v] 之间", elapsed, c.wantMinTime, c.wantMaxTime)
			}
			// 无论谁先完成，回填给模型的结果都必须按模型给出的顺序
			msgs := m.calls[1].Messages
			if msgs[len(msgs)-2].ToolCallID != "1" || msgs[len(msgs)-1].ToolCallID != "2" {
				t.Errorf("回填顺序不对: %+v", msgs[len(msgs)-2:])
			}
			if c.exec == "" && !c.slowSeq && endOrder[0] != "fast" {
				t.Errorf("并行时 tool_execution_end 应按完成顺序，实际 %v", endOrder)
			}
		})
	}
}

// ---- 钩子 ----

func TestLoop_TransformContextAndAfterToolCall(t *testing.T) {
	executed := 0
	m := &scriptedModel{replies: []llm.Message{callTool("c1", "echo", `{"text":"secret-123"}`), {Content: "ok"}}}
	history := []llm.Message{userMsg("很早的消息1"), userMsg("很早的消息2")}
	cfg := Config{
		Model: m,
		Tools: []Tool{echoTool(&executed)},
		// 只把最后 2 条发给模型
		TransformContext: func(_ context.Context, msgs []llm.Message) ([]llm.Message, error) {
			if len(msgs) > 2 {
				msgs = msgs[len(msgs)-2:]
			}
			return msgs, nil
		},
		// 脱敏
		AfterToolCall: func(_ context.Context, _ llm.ToolCall, res ToolResult, _ bool) ToolResult {
			res.Content = strings.ReplaceAll(res.Content, "secret-123", "***")
			return res
		},
	}
	got, err := Loop(context.Background(), cfg, history, userMsg("go"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.calls[0].Messages); n != 2 {
		t.Errorf("TransformContext 后发给模型 %d 条，期望 2", n)
	}
	if len(got) != 4 {
		t.Errorf("TransformContext 不应影响返回的新消息，得到 %d 条", len(got))
	}
	if got[2].Content != "echo: ***" {
		t.Errorf("AfterToolCall 脱敏无效: %q", got[2].Content)
	}
}

func TestLoop_TerminateSkipsNextModelCall(t *testing.T) {
	stop := Tool{Name: "stop", Execute: func(context.Context, string, json.RawMessage, UpdateFunc) (ToolResult, error) {
		return ToolResult{Content: "已提交", Terminate: true}, nil
	}}
	m := &scriptedModel{replies: []llm.Message{callTool("c1", "stop", `{}`)}}
	if _, err := Loop(context.Background(), Config{Model: m, Tools: []Tool{stop}}, nil, userMsg("go")); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 {
		t.Errorf("Terminate 后不应再问模型，实际调用 %d 次", len(m.calls))
	}
}

// ---- 插话与排队 ----

func once(msgs ...llm.Message) func() []llm.Message {
	done := false
	return func() []llm.Message {
		if done {
			return nil
		}
		done = true
		return msgs
	}
}

func TestLoop_SteeringAndFollowUp(t *testing.T) {
	t.Run("插话在工具执行后、下一次问模型前插入", func(t *testing.T) {
		executed := 0
		m := &scriptedModel{replies: []llm.Message{callTool("c1", "echo", `{"text":"a"}`), {Content: "改用中文回答"}}}
		cfg := Config{Model: m, Tools: []Tool{echoTool(&executed)}, GetSteeringMessages: once(userMsg("请用中文"))}
		if _, err := Loop(context.Background(), cfg, nil, userMsg("go")); err != nil {
			t.Fatal(err)
		}
		msgs := m.calls[1].Messages
		if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || last.Content != "请用中文" {
			t.Errorf("第二次问模型时，最后一条应是插话，实际: %+v", last)
		}
	})
	t.Run("排队的问题在回答结束后继续处理", func(t *testing.T) {
		m := &scriptedModel{replies: []llm.Message{{Content: "答1"}, {Content: "答2"}}}
		cfg := Config{Model: m, GetFollowUpMessages: once(userMsg("问2"))}
		got, err := Loop(context.Background(), cfg, nil, userMsg("问1"))
		if err != nil {
			t.Fatal(err)
		}
		if len(m.calls) != 2 || len(got) != 4 || got[3].Content != "答2" {
			t.Errorf("排队问题没有被处理: calls=%d msgs=%+v", len(m.calls), got)
		}
	})
}
