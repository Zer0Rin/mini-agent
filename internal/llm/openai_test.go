package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 测试全部使用本地假服务器（httptest），不访问真实 API、不花钱。

var fastRetry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}

// 格式转换：检查发出去的请求，并解析返回的工具调用
func TestOpenAICompat_Conversion(t *testing.T) {
	var got oaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization 头不对: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"datetime","arguments":"{\"tz\":\"UTC\"}"}}]}}]}`))
	}))
	defer srv.Close()

	c := &OpenAICompat{BaseURL: srv.URL, APIKey: "sk-test", Model: "m", Retry: fastRetry}
	reply, err := c.Complete(context.Background(), Context{
		SystemPrompt: "你是助手",
		Messages: []Message{
			{Role: RoleUser, Content: "几点了"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c0", Name: "datetime", ArgsJSON: `{}`}}},
			{Role: RoleToolResult, ToolCallID: "c0", Content: "bad tz", IsError: true},
		},
		Tools: []ToolDef{{Name: "datetime", Description: "当前时间"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 请求：system 在最前，toolResult 转成 role=tool，错误加前缀
	if len(got.Messages) != 4 || got.Messages[0].Role != "system" {
		t.Fatalf("请求消息不对: %+v", got.Messages)
	}
	tool := got.Messages[3]
	if tool.Role != "tool" || tool.ToolCallID != "c0" || tool.Content != "[error] bad tz" {
		t.Errorf("tool 消息转换不对: %+v", tool)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Parameters == nil {
		t.Errorf("工具定义转换不对: %+v", got.Tools)
	}

	// 响应：解析出工具调用
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "call_1" || reply.ToolCalls[0].ArgsJSON != `{"tz":"UTC"}` {
		t.Errorf("工具调用解析不对: %+v", reply.ToolCalls)
	}
}

// 重试：哪些状态码重试、重试几次
func TestOpenAICompat_Retry(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int // 服务器依次返回的状态码（用完后一直返回最后一个）
		wantCalls int
		wantErr   bool
	}{
		{"两次 500 后成功", []int{500, 500, 200}, 3, false},
		{"429 后成功", []int{429, 200}, 2, false},
		{"401 不重试", []int{401}, 1, true},
		{"一直 503，最多 3 次", []int{503}, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := c.statuses[min(calls, len(c.statuses)-1)]
				calls++
				if status != 200 {
					w.WriteHeader(status)
					return
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer srv.Close()

			cl := &OpenAICompat{BaseURL: srv.URL, Retry: fastRetry}
			_, err := cl.Complete(context.Background(), Context{})
			if calls != c.wantCalls {
				t.Errorf("请求次数 = %d，期望 %d", calls, c.wantCalls)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v，wantErr = %v", err, c.wantErr)
			}
			var apiErr *APIError
			if c.wantErr && !errors.As(err, &apiErr) {
				t.Errorf("错误应能用 errors.As 取出 *APIError，实际: %v", err)
			}
		})
	}
}
