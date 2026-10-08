package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAICompat 调用 OpenAI 兼容接口的模型（DeepSeek、通义、Ollama 等）。
// 同时实现 Model 和 StreamModel。
type OpenAICompat struct {
	BaseURL string // 例如 https://api.deepseek.com
	APIKey  string
	Model   string // 模型名，例如 deepseek-flash

	HTTP  *http.Client // 为空时使用默认客户端（非流式 60s 超时；流式不设整体超时，靠 ctx 控制）
	Retry RetryPolicy  // 零值时使用 DefaultRetry
}

// RetryPolicy 重试策略。
type RetryPolicy struct {
	MaxAttempts int           // 最多尝试几次（含第一次）
	BaseDelay   time.Duration // 第一次重试前等多久，之后翻倍
	MaxDelay    time.Duration
}

// DefaultRetry 默认：最多 3 次，500ms 起步，上限 5s。
var DefaultRetry = RetryPolicy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second}

// APIError 上游返回的非 200 响应。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("api status %d: %s", e.Status, e.Body) }

// retryable 只有限流、服务端错误、网络错误值得重试；4xx（如 401 Key 错误）重试也没用。
func retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// withRetry 按策略重试 fn。fn 返回 started=true 表示已经向外输出过内容（流式），
// 此时即使出错也不能重试 —— 重来一遍会让调用方收到重复的片段。
func (c *OpenAICompat) withRetry(ctx context.Context, fn func() (started bool, err error)) error {
	p := c.Retry
	if p.MaxAttempts <= 0 {
		p = DefaultRetry
	}
	var lastErr error
	for attempt := 0; attempt < p.MaxAttempts; attempt++ {
		started, err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if started || !retryable(err) || attempt == p.MaxAttempts-1 {
			break // 最后一次失败后不再等待（nexus-agent 第二章的坑）
		}
		delay := p.BaseDelay << attempt
		if p.MaxDelay > 0 && delay > p.MaxDelay {
			delay = p.MaxDelay
		}
		delay += time.Duration(rand.Int63n(int64(delay)/4 + 1)) // 抖动，避免惊群
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

// Complete 非流式调用（实现 Model）。
func (c *OpenAICompat) Complete(ctx context.Context, in Context) (Message, error) {
	body, err := json.Marshal(c.buildRequest(in, false))
	if err != nil {
		return Message{}, err
	}
	var out Message
	err = c.withRetry(ctx, func() (bool, error) {
		resp, err := c.post(ctx, body, false)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, err
		}
		var r oaResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return false, fmt.Errorf("decode response: %w", err)
		}
		if len(r.Choices) == 0 {
			return false, errors.New("empty choices")
		}
		out = fromOA(r.Choices[0].Message)
		out.Usage = r.Usage.toUsage()
		return false, nil
	})
	return out, err
}

// Stream 流式调用（实现 StreamModel）。
// 只在"还没收到任何片段"时重试；一旦开始输出，中途断开就直接返回错误。
func (c *OpenAICompat) Stream(ctx context.Context, in Context, onDelta func(Delta)) (Message, error) {
	body, err := json.Marshal(c.buildRequest(in, true))
	if err != nil {
		return Message{}, err
	}
	var out Message
	err = c.withRetry(ctx, func() (bool, error) {
		resp, err := c.post(ctx, body, true)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		var started bool
		out, started, err = readStream(resp.Body, onDelta)
		return started, err
	})
	return out, err
}

// post 发送一次请求；非 200 时读出 body 包装成 *APIError。
func (c *OpenAICompat) post(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
		if !stream {
			client = &http.Client{Timeout: 60 * time.Second}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp, nil
}

// readStream 解析 SSE 流：逐行读取 "data: {...}"，直到 "data: [DONE]"。
// 工具调用的参数是分片到达的，按 index 拼接。
func readStream(r io.Reader, onDelta func(Delta)) (msg Message, started bool, err error) {
	type callAgg struct {
		id, name string
		args     strings.Builder
	}
	var content, reasoning strings.Builder
	calls := map[int]*callAgg{}
	var usage *Usage

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	done := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // 空行、注释行（": keep-alive"）
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk oaStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, started, fmt.Errorf("decode chunk: %w", err)
		}
		if u := chunk.Usage.toUsage(); u != nil {
			usage = u
		}
		for _, ch := range chunk.Choices {
			d := Delta{Content: ch.Delta.Content, Reasoning: ch.Delta.ReasoningContent}
			if d.Content != "" || d.Reasoning != "" {
				started = true
				content.WriteString(d.Content)
				reasoning.WriteString(d.Reasoning)
				if onDelta != nil {
					onDelta(d)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				started = true
				a := calls[tc.Index]
				if a == nil {
					a = &callAgg{}
					calls[tc.Index] = a
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function.Name != "" {
					a.name = tc.Function.Name
				}
				a.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, started, err
	}
	if !done {
		return Message{}, started, io.ErrUnexpectedEOF // 流在 [DONE] 之前断开
	}

	msg = Message{Role: RoleAssistant, Content: content.String(), Reasoning: reasoning.String(), Usage: usage}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		a := calls[i]
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: a.id, Name: a.name, ArgsJSON: a.args.String()})
	}
	return msg, started, nil
}

// ---- 内部 Message ⇄ OpenAI 格式 ----
// 外部 API 的格式只在这个文件里出现，内部数据结构不受它影响。

type oaRequest struct {
	Model         string         `json:"model"`
	Messages      []oaMessage    `json:"messages"`
	Tools         []oaTool       `json:"tools,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
}

type oaMessage struct {
	Role             string       `json:"role"`
	Content          string       `json:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty"`
	ToolCalls        []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    int    `json:"index,omitempty"` // 只在流式片段里出现
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u *oaUsage) toUsage() *Usage {
	if u == nil || u.PromptTokens+u.CompletionTokens == 0 {
		return nil
	}
	return &Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

type oaResponse struct {
	Choices []struct {
		Message oaMessage `json:"message"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
}

type oaStreamChunk struct {
	Choices []struct {
		Delta oaMessage `json:"delta"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
}

func (c *OpenAICompat) buildRequest(in Context, stream bool) oaRequest {
	req := oaRequest{Model: c.Model, Stream: stream}
	if stream {
		req.StreamOptions = map[string]any{"include_usage": true}
	}
	if in.SystemPrompt != "" {
		req.Messages = append(req.Messages, oaMessage{Role: "system", Content: in.SystemPrompt})
	}
	for _, m := range in.Messages {
		req.Messages = append(req.Messages, toOA(m))
	}
	for _, t := range in.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		if ot.Function.Parameters == nil {
			ot.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, ot)
	}
	return req
}

func toOA(m Message) oaMessage {
	switch m.Role {
	case RoleToolResult:
		content := m.Content
		if m.IsError {
			content = "[error] " + content // OpenAI 格式没有 is_error 字段，用前缀告诉模型
		}
		return oaMessage{Role: "tool", Content: content, ToolCallID: m.ToolCallID}
	case RoleAssistant:
		om := oaMessage{Role: "assistant", Content: m.Content}
		for _, tc := range m.ToolCalls {
			var otc oaToolCall
			otc.ID, otc.Type = tc.ID, "function"
			otc.Function.Name, otc.Function.Arguments = tc.Name, tc.ArgsJSON
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		return om
	default:
		return oaMessage{Role: m.Role, Content: m.Content}
	}
}

func fromOA(om oaMessage) Message {
	m := Message{Role: RoleAssistant, Content: om.Content, Reasoning: om.ReasoningContent}
	for _, tc := range om.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, ArgsJSON: tc.Function.Arguments})
	}
	return m
}
