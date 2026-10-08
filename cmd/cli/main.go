// 命令行对话入口。
//
// 用法：
//
//	export DEEPSEEK_API_KEY=sk-xxx
//	go run ./cmd/cli
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/tool"
)

func main() {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "请先设置环境变量 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	model := &llm.OpenAICompat{
		BaseURL: envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		APIKey:  key,
		Model:   envOr("DEEPSEEK_MODEL", "deepseek-flash"), // 和 nexus-agent 里用的模型名一致
	}

	cfg := agent.Config{
		Model:        model,
		SystemPrompt: "你是一个简洁的助手。需要实时信息时使用工具，不要编造。",
		Tools:        []agent.Tool{tool.Datetime()},
		MaxTurns:     8,
		OnEvent:      printEvent,
	}

	// Ctrl+C 取消当前这一轮，而不是直接杀掉进程
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var history []llm.Message
	in := bufio.NewScanner(os.Stdin)
	fmt.Println("mini-agent 已启动，输入 exit 退出")
	for {
		fmt.Print("\n你> ")
		if !in.Scan() {
			return
		}
		text := strings.TrimSpace(in.Text())
		if text == "" {
			continue
		}
		if text == "exit" {
			return
		}

		newMsgs, err := agent.Loop(ctx, cfg, history, llm.Message{Role: llm.RoleUser, Content: text})
		history = append(history, newMsgs...) // 出错也保留已产生的消息，下一轮还能接着聊
		if err != nil {
			fmt.Println("出错了:", err)
		}
	}
}

// printEvent 把事件打印到终端：看得到 Agent 每一步在做什么。
func printEvent(_ context.Context, e agent.Event) {
	switch e.Type {
	case agent.EventToolExecutionStart:
		fmt.Printf("  ⚙ 调用 %s %s\n", e.ToolName, string(e.Args))
	case agent.EventToolExecutionEnd:
		mark := "✓"
		if e.IsError {
			mark = "✗"
		}
		fmt.Printf("  %s %s → %s\n", mark, e.ToolName, e.Result.Content)
	case agent.EventMessageEnd:
		if e.Message.Role == llm.RoleAssistant && e.Message.Content != "" {
			fmt.Printf("\nAgent> %s\n", e.Message.Content)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
