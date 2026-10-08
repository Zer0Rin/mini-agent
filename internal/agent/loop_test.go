package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Zer0Rin/mini-agent/internal/llm"
)

// ---- 假模型：按剧本依次返回回复，并记录每次收到的输入 ----

type scriptedModel struct {
	replies []llm.Message
	calls   []llm.Context
}

func (m *scriptedModel) Complete(_ context.Context, c llm.Context) (llm.Message, error) {
	m.calls = append(m.calls, c)
	if len(m.calls) > len(m.replies) {
		return llm.Message{}, errors.New("scriptedModel: 剧本用完了")
	}
	return m.replies[len(m.calls)-1], nil
}

// alwaysCallTool 永远要求调用工具的模型（测试最大轮数）。
type alwaysCallTool struct{ n int }

func (m *alwaysCallTool) Complete(context.Context, llm.Context) (llm.Message, error) {
	m.n++
	return llm.Message{ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", ArgsJSON: `{}`}}}, nil
}

// ---- 测试用工具 ----

func echoTool(executed *int) Tool {
	return Tool{
		Name: "echo",
		Execute: func(_ context.Context, _ string, args json.RawMessage, _ UpdateFunc) (ToolResult, error) {
			*executed++
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: "echo: " + p.Text}, nil
		},
	}
}

func userMsg(s string) llm.Message { return llm.Message{Role: llm.RoleUser, Content: s} }

func callTool(id, name, args string) llm.Message {
	return llm.Message{ToolCalls: []llm.ToolCall{{ID: id, Name: name, ArgsJSON: args}}}
}

// ---- 测试 ----

func TestLoop_DirectAnswer(t *testing.T) {
	m := &scriptedModel{replies: []llm.Message{{Content: "你好"}}}
	got, err := Loop(context.Background(), Config{Model: m}, nil, userMsg("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 {
		t.Errorf("模型调用次数 = %d，期望 1", len(m.calls))
	}
	if len(got) != 2 || got[1].Role != llm.RoleAssistant || got[1].Content != "你好" {
		t.Errorf("新增消息不对: %+v", got)
	}
}

func TestLoop_ToolRoundTrip(t *testing.T) {
	executed := 0
	m := &scriptedModel{replies: []llm.Message{
		callTool("c1", "echo", `{"text":"ping"}`),
		{Content: "工具说了 ping"},
	}}
	got, err := Loop(context.Background(), Config{Model: m, Tools: []Tool{echoTool(&executed)}}, nil, userMsg("试试工具"))
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Errorf("工具执行次数 = %d，期望 1", executed)
	}
	// 第二次问模型时，最后一条消息必须是配对正确的工具结果
	last := m.calls[1].Messages[len(m.calls[1].Messages)-1]
	if last.Role != llm.RoleToolResult || last.ToolCallID != "c1" || last.Content != "echo: ping" || last.IsError {
		t.Errorf("回填的工具结果不对: %+v", last)
	}
	if len(got) != 4 { // user, assistant(调用), toolResult, assistant(回答)
		t.Errorf("新增消息条数 = %d，期望 4", len(got))
	}
}

// 工具出错的各种情况：都不能中断循环，都要以 IsError 交给模型
func TestLoop_ToolErrorsBecomeIsError(t *testing.T) {
	panicTool := Tool{Name: "boom", Execute: func(context.Context, string, json.RawMessage, UpdateFunc) (ToolResult, error) {
		var m map[string]int
		m["x"] = 1 // 往 nil map 写入 → panic
		return ToolResult{}, nil
	}}

	cases := []struct {
		name     string
		call     llm.Message
		block    bool
		wantExec int // echo 工具应被执行的次数
	}{
		{"未注册的工具", callTool("c1", "db_query", `{}`), false, 0},
		{"参数类型错误", callTool("c1", "echo", `{"text":123}`), false, 1},
		{"工具 panic", callTool("c1", "boom", `{}`), false, 0},
		{"被钩子拦截", callTool("c1", "echo", `{"text":"x"}`), true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			executed := 0
			m := &scriptedModel{replies: []llm.Message{c.call, {Content: "好的"}}}
			cfg := Config{
				Model: m,
				Tools: []Tool{echoTool(&executed), panicTool},
				BeforeToolCall: func(context.Context, llm.ToolCall) (bool, string) {
					return c.block, "需要人工审批"
				},
			}
			_, err := Loop(context.Background(), cfg, nil, userMsg("go"))
			if err != nil {
				t.Fatalf("循环不应报错，实际: %v", err)
			}
			if executed != c.wantExec {
				t.Errorf("echo 执行次数 = %d，期望 %d", executed, c.wantExec)
			}
			res := m.calls[1].Messages[len(m.calls[1].Messages)-1]
			if !res.IsError || res.Content == "" {
				t.Errorf("期望 IsError=true 且带错误描述，实际: %+v", res)
			}
		})
	}
}

// 边界：MaxTurns=3 时模型恰好被调用 3 次（不是 2 次，也不是 4 次）
func TestLoop_MaxTurns(t *testing.T) {
	executed := 0
	m := &alwaysCallTool{}
	_, err := Loop(context.Background(), Config{Model: m, Tools: []Tool{echoTool(&executed)}, MaxTurns: 3}, nil, userMsg("loop"))
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("err = %v，期望 ErrMaxTurns", err)
	}
	if m.n != 3 {
		t.Errorf("模型调用次数 = %d，期望 3", m.n)
	}
}

// 事件顺序：一次工具往返应产生的完整事件序列
func TestLoop_EventOrder(t *testing.T) {
	executed := 0
	m := &scriptedModel{replies: []llm.Message{callTool("c1", "echo", `{"text":"a"}`), {Content: "done"}}}
	var got []EventType
	cfg := Config{
		Model:   m,
		Tools:   []Tool{echoTool(&executed)},
		OnEvent: func(_ context.Context, e Event) { got = append(got, e.Type) },
	}
	if _, err := Loop(context.Background(), cfg, nil, userMsg("x")); err != nil {
		t.Fatal(err)
	}
	want := []EventType{
		EventAgentStart,
		EventMessageStart, EventMessageEnd, // user
		EventTurnStart,
		EventMessageStart, EventMessageEnd, // assistant（调用工具）
		EventToolExecutionStart, EventToolExecutionEnd,
		EventMessageStart, EventMessageEnd, // toolResult
		EventTurnEnd,
		EventTurnStart,
		EventMessageStart, EventMessageEnd, // assistant（回答）
		EventTurnEnd,
		EventAgentEnd,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("事件顺序不对\n得到: %v\n期望: %v", got, want)
	}
}
