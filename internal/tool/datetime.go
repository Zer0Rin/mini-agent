// Package tool 内置工具。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Zer0Rin/mini-agent/internal/agent"
)

// Datetime 查询指定时区的当前时间。
func Datetime() agent.Tool {
	return agent.Tool{
		Name:        "datetime",
		Label:       "当前时间",
		Description: "获取某个时区的当前日期和时间。用户问时间、日期、星期几时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tz": map[string]any{
					"type":        "string",
					"description": "IANA 时区名，例如 America/Los_Angeles、Asia/Shanghai。不填则为 UTC",
				},
			},
		},
		Execute: func(_ context.Context, _ string, args json.RawMessage, _ agent.UpdateFunc) (agent.ToolResult, error) {
			var p struct {
				TZ string `json:"tz"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return agent.ToolResult{}, fmt.Errorf("invalid args: %w", err) // 类型不对直接报错，不静默用默认值
			}
			if p.TZ == "" {
				p.TZ = "UTC"
			}
			loc, err := time.LoadLocation(p.TZ)
			if err != nil {
				return agent.ToolResult{}, fmt.Errorf("unknown timezone %q", p.TZ)
			}
			now := time.Now().In(loc)
			return agent.ToolResult{Content: now.Format("2006-01-02 15:04:05 Monday MST")}, nil
		},
	}
}
