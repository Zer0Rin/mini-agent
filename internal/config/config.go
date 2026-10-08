// Package config 从环境变量读取配置。所有密钥只通过环境变量传入，不写进代码和仓库。
package config

import (
	"errors"
	"os"
	"strconv"
)

// Config 运行配置。
type Config struct {
	Addr     string // 监听地址，默认只监听本机
	Token    string // 非空时 API 需要 Bearer Token
	Store    string // sqlite 或 memory
	DBPath   string
	MaxTurns int

	ModelBaseURL string
	ModelAPIKey  string
	ModelName    string
	SystemPrompt string
}

// Load 读取环境变量并校验。
func Load() (Config, error) {
	c := Config{
		Addr:         env("MINI_AGENT_ADDR", "127.0.0.1:8080"),
		Token:        os.Getenv("MINI_AGENT_TOKEN"),
		Store:        env("MINI_AGENT_STORE", "sqlite"),
		DBPath:       env("MINI_AGENT_DB", "data/mini-agent.db"),
		MaxTurns:     envInt("MINI_AGENT_MAX_TURNS", 10),
		ModelBaseURL: env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		ModelAPIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		ModelName:    env("DEEPSEEK_MODEL", "deepseek-flash"),
		SystemPrompt: env("MINI_AGENT_SYSTEM_PROMPT", "你是一个简洁的助手。需要实时信息时使用工具，不要编造。"),
	}
	if c.ModelAPIKey == "" {
		return c, errors.New("DEEPSEEK_API_KEY is required")
	}
	if c.Store != "sqlite" && c.Store != "memory" {
		return c, errors.New("MINI_AGENT_STORE must be sqlite or memory")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
