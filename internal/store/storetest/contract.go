// Package storetest Store 接口的契约测试：任何实现（内存、SQLite、以后的 PostgreSQL）
// 都必须通过同一套测试，保证行为一致。
package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/store"
)

// Run 对 newStore 创建的实现执行全部契约测试。每个子测试都拿到一个全新的空存储。
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Run("会话增删改查", func(t *testing.T) { testSessions(t, newStore(t)) })
	t.Run("用户隔离", func(t *testing.T) { testIsolation(t, newStore(t)) })
	t.Run("消息树与分支", func(t *testing.T) { testBranching(t, newStore(t)) })
	t.Run("消息字段完整保存", func(t *testing.T) { testRoundTrip(t, newStore(t)) })
	t.Run("并发追加", func(t *testing.T) { testConcurrentAppend(t, newStore(t)) })
}

var ctx = context.Background()

func user(s string) llm.Message { return llm.Message{Role: llm.RoleUser, Content: s} }

// must 出错时直接 panic（测试会标记为失败并打印调用栈）。
// Go 不允许 f(t, g()) 这种"额外参数 + 多返回值"的写法，所以这里不接收 t。
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func testSessions(t *testing.T, s store.Store) {
	a := must(s.CreateSession(ctx, "u1", "第一个"))
	time.Sleep(2 * time.Millisecond)
	b := must(s.CreateSession(ctx, "u1", "第二个"))

	list := must(s.ListSessions(ctx, "u1", 10))
	if len(list) != 2 || list[0].ID != b.ID {
		t.Fatalf("应按更新时间倒序，实际 %+v", list)
	}
	if err := s.RenameSession(ctx, "u1", a.ID, "改名"); err != nil {
		t.Fatal(err)
	}
	if got := must(s.GetSession(ctx, "u1", a.ID)); got.Title != "改名" {
		t.Errorf("改名无效: %+v", got)
	}
	if list := must(s.ListSessions(ctx, "u1", 1)); len(list) != 1 || list[0].ID != a.ID {
		t.Errorf("改名后应排到最前，且 limit 生效，实际 %+v", list)
	}

	must(s.AppendMessage(ctx, "u1", a.ID, "", user("hi")))
	if err := s.DeleteSession(ctx, "u1", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "u1", a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("删除后应 ErrNotFound，实际 %v", err)
	}
	if _, err := s.Path(ctx, "u1", a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("删除后消息也应不可访问，实际 %v", err)
	}
}

func testIsolation(t *testing.T, s store.Store) {
	se := must(s.CreateSession(ctx, "alice", "alice 的会话"))
	r := must(s.AppendMessage(ctx, "alice", se.ID, "", user("秘密")))

	// bob 对 alice 的会话做任何操作都应该是 ErrNotFound
	checks := map[string]error{
		"Get":    func() error { _, err := s.GetSession(ctx, "bob", se.ID); return err }(),
		"Path":   func() error { _, err := s.Path(ctx, "bob", se.ID); return err }(),
		"Append": func() error { _, err := s.AppendMessage(ctx, "bob", se.ID, r.ID, user("x")); return err }(),
		"Rename": s.RenameSession(ctx, "bob", se.ID, "hack"),
		"Head":   s.SetHead(ctx, "bob", se.ID, ""),
		"Delete": s.DeleteSession(ctx, "bob", se.ID),
	}
	for name, err := range checks {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("bob %s alice 的会话应返回 ErrNotFound，实际 %v", name, err)
		}
	}
	if list := must(s.ListSessions(ctx, "bob", 10)); len(list) != 0 {
		t.Errorf("bob 不应看到 alice 的会话: %+v", list)
	}
	// alice 的数据没有被改动
	if got := must(s.GetSession(ctx, "alice", se.ID)); got.Title != "alice 的会话" || got.HeadID != r.ID {
		t.Errorf("alice 的会话被改动了: %+v", got)
	}
}

func testBranching(t *testing.T, s store.Store) {
	se := must(s.CreateSession(ctx, "u", "branch"))
	q1 := must(s.AppendMessage(ctx, "u", se.ID, "", user("问1")))
	a1 := must(s.AppendMessage(ctx, "u", se.ID, q1.ID, llm.Message{Role: llm.RoleAssistant, Content: "答1"}))
	q2 := must(s.AppendMessage(ctx, "u", se.ID, a1.ID, user("问2")))

	contents := func() []string {
		var out []string
		for _, r := range must(s.Path(ctx, "u", se.ID)) {
			out = append(out, r.Message.Content)
		}
		return out
	}
	if got := contents(); len(got) != 3 || got[2] != "问2" {
		t.Fatalf("主分支 = %v", got)
	}

	// 回到 答1，重新提问 → 新分支
	if err := s.SetHead(ctx, "u", se.ID, a1.ID); err != nil {
		t.Fatal(err)
	}
	must(s.AppendMessage(ctx, "u", se.ID, a1.ID, user("换个问法")))
	if got := contents(); len(got) != 3 || got[2] != "换个问法" {
		t.Errorf("新分支 = %v", got)
	}
	// 旧分支还在：把 head 移回 q2
	if err := s.SetHead(ctx, "u", se.ID, q2.ID); err != nil {
		t.Fatal(err)
	}
	if got := contents(); got[2] != "问2" {
		t.Errorf("旧分支丢了: %v", got)
	}
	// 回到开头
	if err := s.SetHead(ctx, "u", se.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := contents(); len(got) != 0 {
		t.Errorf("回到开头后路径应为空: %v", got)
	}

	// 父消息必须属于同一会话
	other := must(s.CreateSession(ctx, "u", "other"))
	if _, err := s.AppendMessage(ctx, "u", other.ID, q1.ID, user("x")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("跨会话挂父消息应 ErrNotFound，实际 %v", err)
	}
	if err := s.SetHead(ctx, "u", other.ID, q1.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetHead 到别的会话的消息应 ErrNotFound，实际 %v", err)
	}
}

func testRoundTrip(t *testing.T, s store.Store) {
	se := must(s.CreateSession(ctx, "u", "rt"))
	in := []llm.Message{
		{Role: llm.RoleUser, Content: "现在几点"},
		{Role: llm.RoleAssistant, Reasoning: "要查时间", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "datetime", ArgsJSON: `{"tz":"UTC"}`}},
			Usage: &llm.Usage{InputTokens: 12, OutputTokens: 3}},
		{Role: llm.RoleToolResult, ToolCallID: "c1", ToolName: "datetime", Content: "bad tz", IsError: true},
	}
	parent := ""
	for _, m := range in {
		parent = must(s.AppendMessage(ctx, "u", se.ID, parent, m)).ID
	}
	got := must(s.Path(ctx, "u", se.ID))
	if len(got) != len(in) {
		t.Fatalf("条数 %d，期望 %d", len(got), len(in))
	}
	a := got[1].Message
	if a.Reasoning != "要查时间" || len(a.ToolCalls) != 1 || a.ToolCalls[0].ArgsJSON != `{"tz":"UTC"}` ||
		a.Usage == nil || a.Usage.InputTokens != 12 {
		t.Errorf("assistant 消息字段丢失: %+v", a)
	}
	tr := got[2].Message
	if tr.ToolCallID != "c1" || tr.ToolName != "datetime" || !tr.IsError {
		t.Errorf("toolResult 字段丢失: %+v", tr)
	}
	if got[1].ParentID != got[0].ID || got[0].ParentID != "" {
		t.Errorf("父子关系不对: %+v", got)
	}
}

func testConcurrentAppend(t *testing.T, s store.Store) {
	se := must(s.CreateSession(ctx, "u", "c"))
	root := must(s.AppendMessage(ctx, "u", se.ID, "", user("root")))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AppendMessage(ctx, "u", se.ID, root.ID, user("child")); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发追加失败: %v", err)
	}
	// head 指向其中某个 child，路径长度应为 2
	if p := must(s.Path(ctx, "u", se.ID)); len(p) != 2 {
		t.Errorf("并发追加后路径长度 %d，期望 2", len(p))
	}
}
