package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sse 把若干 JSON 片段写成 SSE 响应。
func sse(w http.ResponseWriter, chunks []string, done bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
	if done {
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func TestStream_AggregatesDeltasAndToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, []string{
			`{"choices":[{"delta":{"reasoning_content":"想一想"}}]}`,
			`{"choices":[{"delta":{"content":"我来"}}]}`,
			`{"choices":[{"delta":{"content":"查一下"}}]}`,
			// 工具调用参数分三片到达
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"datetime","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"tz\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"UTC\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		}, true)
	}))
	defer srv.Close()

	var deltas []string
	c := &OpenAICompat{BaseURL: srv.URL, Retry: fastRetry}
	msg, err := c.Stream(context.Background(), Context{}, func(d Delta) {
		deltas = append(deltas, d.Reasoning+d.Content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "想一想|我来|查一下" {
		t.Errorf("增量顺序不对: %v", deltas)
	}
	if msg.Content != "我来查一下" || msg.Reasoning != "想一想" {
		t.Errorf("拼接结果不对: %+v", msg)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].ArgsJSON != `{"tz":"UTC"}` {
		t.Errorf("工具调用拼接不对: %+v", msg.ToolCalls)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 10 || msg.Usage.OutputTokens != 5 {
		t.Errorf("用量不对: %+v", msg.Usage)
	}
}

// 还没输出任何内容就失败 → 可以重试；已经输出了 → 不能重试（否则调用方会收到重复片段）
func TestStream_RetryOnlyBeforeFirstDelta(t *testing.T) {
	cases := []struct {
		name      string
		handler   func(call int, w http.ResponseWriter)
		wantCalls int
		wantErr   bool
	}{
		{"首次 503，第二次成功", func(call int, w http.ResponseWriter) {
			if call == 1 {
				w.WriteHeader(503)
				return
			}
			sse(w, []string{`{"choices":[{"delta":{"content":"ok"}}]}`}, true)
		}, 2, false},
		{"输出一半后断开，不重试", func(call int, w http.ResponseWriter) {
			sse(w, []string{`{"choices":[{"delta":{"content":"半句"}}]}`}, false)
		}, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				c.handler(calls, w)
			}))
			defer srv.Close()

			cl := &OpenAICompat{BaseURL: srv.URL, Retry: fastRetry}
			_, err := cl.Stream(context.Background(), Context{}, nil)
			if calls != c.wantCalls {
				t.Errorf("请求次数 = %d，期望 %d", calls, c.wantCalls)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v，wantErr = %v", err, c.wantErr)
			}
		})
	}
}
