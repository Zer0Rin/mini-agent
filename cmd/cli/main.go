// 命令行对话入口（直接使用 Agent，不经过 HTTP 服务）。
//
//	export DEEPSEEK_API_KEY=...
//	go run ./cmd/cli
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/Zer0Rin/mini-agent/internal/agent"
	"github.com/Zer0Rin/mini-agent/internal/config"
	"github.com/Zer0Rin/mini-agent/internal/llm"
	"github.com/Zer0Rin/mini-agent/internal/tool"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a := agent.New(agent.Config{
		Model:        &llm.OpenAICompat{BaseURL: cfg.ModelBaseURL, APIKey: cfg.ModelAPIKey, Model: cfg.ModelName},
		SystemPrompt: cfg.SystemPrompt,
		Tools:        []agent.Tool{tool.Datetime()},
		MaxTurns:     cfg.MaxTurns,
	}, nil)
	a.Subscribe(printEvent)

	// Ctrl+C：运行中则中断这一轮，空闲时退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		for range sig {
			if a.IsRunning() {
				a.Abort()
			} else {
				os.Exit(0)
			}
		}
	}()

	in := bufio.NewScanner(os.Stdin)
	fmt.Println("mini-agent 已启动。输入 exit 退出，运行中按 Ctrl+C 中断。")
	for {
		fmt.Print("\n你> ")
		if !in.Scan() {
			return
		}
		text := strings.TrimSpace(in.Text())
		switch text {
		case "":
			continue
		case "exit":
			return
		}
		err := a.Prompt(context.Background(), llm.Message{Role: llm.RoleUser, Content: text})
		switch {
		case errors.Is(err, context.Canceled):
			fmt.Println("\n（已中断）")
		case err != nil:
			fmt.Println("\n出错了:", err)
		}
	}
}

// printEvent 流式打印：思考过程灰色显示，正文逐字输出，工具调用单独一行。
func printEvent(_ context.Context, e agent.Event) {
	const gray, reset = "\033[90m", "\033[0m"
	switch e.Type {
	case agent.EventMessageStart:
		if e.Message.Role == llm.RoleAssistant {
			fmt.Print("\nAgent> ")
		}
	case agent.EventMessageUpdate:
		if e.Delta.Reasoning != "" {
			fmt.Print(gray + e.Delta.Reasoning + reset)
		}
		fmt.Print(e.Delta.Content)
	case agent.EventToolExecutionStart:
		fmt.Printf("\n  ⚙ 调用 %s %s", e.ToolName, string(e.Args))
	case agent.EventToolExecutionEnd:
		mark := "✓"
		if e.IsError {
			mark = "✗"
		}
		fmt.Printf("\n  %s %s → %s\n", mark, e.ToolName, e.Result.Content)
	}
}
