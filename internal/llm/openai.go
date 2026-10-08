package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat 调用 OpenAI 兼容接口的模型（DeepSeek、通义、Ollama 等）。
type OpenAICompat struct {
	BaseURL string // 例如 https://api.deepseek.com
	APIKey  string
	Model   string // 模型名，例如 deepseek-flash

	HTTP  *http.Client // 为空时使用带 60s 超时的默认客户端
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

// Complete 实现 Model 接口。
func (c *OpenAICompat) Complete(ctx context.Context, in Context) (Message, error) {
	body, err := json.Marshal(c.buildRequest(in))
	if err != nil {
		return Message{}, err
	}

	p := c.Retry
	if p.MaxAttempts <= 0 {
		p = DefaultRetry
	}

	var lastErr error
	for attempt := 0; attempt < p.MaxAttempts; attempt++ {
		msg, err := c.doOnce(ctx, body)
		if err == nil {
			return msg, nil
		}
		lastErr = err
		if !retryable(err) || attempt == p.MaxAttempts-1 {
			break // 不可重试，或已经是最后一次：不再等待（nexus-agent 第二章的坑）
		}
		delay := p.BaseDelay << attempt
		if p.MaxDelay > 0 && delay > p.MaxDelay {
			delay = p.MaxDelay
		}
		delay += time.Duration(rand.Int63n(int64(delay)/4 + 1)) // 抖动，避免惊群
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
	return Message{}, lastErr
}

func (c *OpenAICompat) doOnce(ctx context.Context, body []byte) (Message, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}

	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Message{}, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return Message{}, errors.New("empty choices")
	}
	return fromOA(out.Choices[0].Message), nil
}

// ---- 内部 Message ⇄ OpenAI 格式 ----
// 外部 API 的格式只在这个文件里出现，内部数据结构不受它影响。

type oaRequest struct {
	Model    string      `json:"model"`
	Messages []oaMessage `json:"messages"`
	Tools    []oaTool    `json:"tools,omitempty"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
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

type oaResponse struct {
	Choices []struct {
		Message oaMessage `json:"message"`
	} `json:"choices"`
}

func (c *OpenAICompat) buildRequest(in Context) oaRequest {
	req := oaRequest{Model: c.Model}
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
	m := Message{Role: RoleAssistant, Content: om.Content}
	for _, tc := range om.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, ArgsJSON: tc.Function.Arguments})
	}
	return m
}
