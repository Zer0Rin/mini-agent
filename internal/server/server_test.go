package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/store"
)

// ---- 测试替身 ----

// scripted 按剧本依次回复；剧本用完后一直回复最后一条。
type scripted struct {
	mu      sync.Mutex
	replies []llm.Message
	n       int
}

func (m *scripted) Complete(context.Context, llm.Context) (llm.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.replies[min(m.n, len(m.replies)-1)]
	m.n++
	return r, nil
}

// blocking 一直阻塞到 ctx 取消。
type blocking struct{ started chan struct{} }

func (m *blocking) Complete(ctx context.Context, _ llm.Context) (llm.Message, error) {
	m.started <- struct{}{}
	<-ctx.Done()
	return llm.Message{}, ctx.Err()
}

var echo = agent.Tool{Name: "echo", Execute: func(_ context.Context, _ string, args json.RawMessage, _ agent.UpdateFunc) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "echo " + string(args)}, nil
}}

func newTestServer(t *testing.T, model llm.Model, token string) (*httptest.Server, store.Store) {
	t.Helper()
	st := store.NewMemory()
	srv := New(Options{
		Store:       st,
		AgentConfig: func() agent.Config { return agent.Config{Model: model, Tools: []agent.Tool{echo}} },
		Token:       token,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// ---- HTTP 小工具 ----

func do(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func createSession(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp := do(t, ts, "POST", "/api/sessions", `{}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建会话: %d", resp.StatusCode)
	}
	return decodeBody[wireSession](t, resp).ID
}

// readSSE 读完整个事件流。
func readSSE(t *testing.T, resp *http.Response) []wireEvent {
	t.Helper()
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out []wireEvent
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var e wireEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
	}
	return out
}

type sessionDetail struct {
	Session  wireSession   `json:"session"`
	Messages []wireMessage `json:"messages"`
	Running  bool          `json:"running"`
}

// ---- 测试 ----

func TestChatFlow(t *testing.T) {
	model := &scripted{replies: []llm.Message{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "echo", ArgsJSON: `{"x":1}`}}},
		{Content: "完成"},
	}}
	ts, _ := newTestServer(t, model, "")
	sid := createSession(t, ts)

	events := readSSE(t, do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"帮我调用 echo"}`))

	var types []string
	var saved []*wireMessage
	for _, e := range events {
		types = append(types, e.Type)
		if e.Type == string(agent.EventMessageEnd) {
			saved = append(saved, e.Message)
		}
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{"agent_start", "tool_execution_start", "tool_execution_end", "agent_end"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少事件 %s: %s", want, joined)
		}
	}
	if types[len(types)-1] != eventDone {
		t.Errorf("最后一个事件应是 done，实际 %s", types[len(types)-1])
	}
	// user、assistant(调用)、toolResult、assistant(回答)：每条都带上了落库后的 ID，且串成一条链
	if len(saved) != 4 {
		t.Fatalf("message_end 次数 = %d，期望 4", len(saved))
	}
	for i, m := range saved {
		if m.ID == "" {
			t.Errorf("第 %d 条 message_end 没有 ID", i)
		}
		if i > 0 && m.ParentID != saved[i-1].ID {
			t.Errorf("第 %d 条的 parent_id 不对", i)
		}
	}

	detail := decodeBody[sessionDetail](t, do(t, ts, "GET", "/api/sessions/"+sid, ""))
	if len(detail.Messages) != 4 || detail.Messages[3].Content != "完成" || detail.Running {
		t.Errorf("会话详情不对: %+v", detail)
	}
	if detail.Session.Title != "帮我调用 echo" {
		t.Errorf("第一条消息应自动成为标题，实际 %q", detail.Session.Title)
	}
}

func TestBusySteerAndAbort(t *testing.T) {
	model := &blocking{started: make(chan struct{}, 1)}
	ts, _ := newTestServer(t, model, "")
	sid := createSession(t, ts)

	result := make(chan []wireEvent, 1)
	go func() {
		result <- readSSE(t, do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"慢慢来"}`))
	}()
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("模型没有被调用")
	}

	if code := do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"插队"}`).StatusCode; code != http.StatusConflict {
		t.Errorf("运行中再发消息应 409，实际 %d", code)
	}
	if code := do(t, ts, "DELETE", "/api/sessions/"+sid, "").StatusCode; code != http.StatusConflict {
		t.Errorf("运行中删除会话应 409，实际 %d", code)
	}
	if code := do(t, ts, "POST", "/api/sessions/"+sid+"/steer", `{"content":"顺便说一句"}`).StatusCode; code != http.StatusAccepted {
		t.Errorf("运行中插话应 202，实际 %d", code)
	}
	if code := do(t, ts, "POST", "/api/sessions/"+sid+"/abort", "").StatusCode; code != http.StatusAccepted {
		t.Errorf("中断应 202，实际 %d", code)
	}

	events := <-result
	last2 := events[len(events)-2:]
	if last2[0].Type != eventError || last2[0].Error != "aborted" || last2[1].Type != eventDone {
		t.Errorf("中断后应以 error(aborted) + done 结束，实际 %+v", last2)
	}

	// 中断后：用户消息已保存，会话不再处于运行状态，可以再次中断→409
	detail := decodeBody[sessionDetail](t, do(t, ts, "GET", "/api/sessions/"+sid, ""))
	if len(detail.Messages) != 1 || detail.Messages[0].Content != "慢慢来" || detail.Running {
		t.Errorf("中断后的会话状态不对: %+v", detail)
	}
	if code := do(t, ts, "POST", "/api/sessions/"+sid+"/abort", "").StatusCode; code != http.StatusConflict {
		t.Errorf("没在运行时中断应 409，实际 %d", code)
	}
}

func TestBranching(t *testing.T) {
	model := &scripted{replies: []llm.Message{{Content: "答1"}, {Content: "答2"}, {Content: "答2b"}}}
	ts, _ := newTestServer(t, model, "")
	sid := createSession(t, ts)

	readSSE(t, do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"问1"}`))
	readSSE(t, do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"问2"}`))
	msgs := decodeBody[sessionDetail](t, do(t, ts, "GET", "/api/sessions/"+sid, "")).Messages
	if len(msgs) != 4 {
		t.Fatalf("应有 4 条消息，实际 %d", len(msgs))
	}

	// 回到"答1"，换一个问题 → 新分支
	if code := do(t, ts, "POST", "/api/sessions/"+sid+"/head", `{"message_id":"`+msgs[1].ID+`"}`).StatusCode; code != http.StatusNoContent {
		t.Fatalf("设置 head: %d", code)
	}
	readSSE(t, do(t, ts, "POST", "/api/sessions/"+sid+"/messages", `{"content":"问2b"}`))
	branch := decodeBody[sessionDetail](t, do(t, ts, "GET", "/api/sessions/"+sid, "")).Messages
	if len(branch) != 4 || branch[2].Content != "问2b" || branch[2].ParentID != msgs[1].ID {
		t.Errorf("新分支不对: %+v", branch)
	}
}

func TestAuthAndNotFound(t *testing.T) {
	ts, _ := newTestServer(t, &scripted{replies: []llm.Message{{Content: "x"}}}, "secret")

	if code := do(t, ts, "GET", "/api/sessions", "").StatusCode; code != http.StatusUnauthorized {
		t.Errorf("没带 token 应 401，实际 %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("带正确 token 应 200，实际 %v %v", resp.StatusCode, err)
	}
	if resp, _ := http.Get(ts.URL + "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz 不需要 token，实际 %d", resp.StatusCode)
	}

	ts2, _ := newTestServer(t, &scripted{replies: []llm.Message{{Content: "x"}}}, "")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/sessions/nope", ""},
		{"POST", "/api/sessions/nope/messages", `{"content":"hi"}`},
		{"POST", "/api/sessions/nope/abort", ""},
	} {
		if code := do(t, ts2, c.method, c.path, c.body).StatusCode; code != http.StatusNotFound {
			t.Errorf("%s %s 应 404，实际 %d", c.method, c.path, code)
		}
	}
	if code := do(t, ts2, "POST", "/api/sessions", `not json`).StatusCode; code != http.StatusBadRequest {
		t.Errorf("非法 JSON 应 400，实际 %d", code)
	}
}

func TestErrorMessage(t *testing.T) {
	cases := map[string]error{
		"aborted":                      context.Canceled,
		"max turns exceeded":           agent.ErrMaxTurns,
		"model api error (status 429)": &llm.APIError{Status: 429},
		"agent error":                  errors.New("boom"),
	}
	for want, err := range cases {
		if got := errorMessage(err); got != want {
			t.Errorf("errorMessage(%v) = %q，期望 %q", err, got, want)
		}
	}
}
