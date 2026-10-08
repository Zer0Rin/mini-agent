# mini-agent

一个用 Go 实现的 Agent 平台：参照 [pi-agent-core](https://github.com/badlogic/pi-mono) 的两层 harness（无状态循环 + 有状态外壳），
提供流式 HTTP/SSE 接口、可分支的会话存储（SQLite / 内存），以及 Next.js 前端。

```
浏览器 ──fetch/SSE──▶ Next.js (BFF 代理, 注入 Token) ──▶ Go server ──▶ agent.Agent ──▶ agent.Loop ──▶ LLM (OpenAI 兼容)
                                                          │                             └──▶ Tools
                                                          └──▶ store.Store (SQLite / Memory)
```

## 特性

- **Agent 循环**：流式输出、并行/顺序工具执行、`BeforeToolCall` 拦截与 `AfterToolCall` 改写、
  运行中插话（steering）与排队追问（follow-up）、工具 `Terminate`、`TransformContext`（上下文裁剪入口）、最大轮数保护
- **健壮性**：工具 panic 被转成错误结果；模型调用 429/5xx 指数退避重试，流式只在首个片段前重试；客户端断开时已完成的消息仍会保存
- **会话存储**：消息树（`parent_id` + `head_id`）支持从任意历史消息分叉；所有方法按 `userID` 隔离；一套契约测试同时约束所有实现
- **接口**：REST + SSE，可选 Bearer Token，默认只监听 127.0.0.1，优雅关停

## 快速开始

需要 Go 1.24.7+、Node 20.9+（推荐 Node 24 LTS；前端测试脚本需要 Node 24+）。
SQLite 驱动固定为兼容 Go 1.24 的 `modernc.org/sqlite v1.38.2`；前端使用 Next.js 16 / React 19。

```bash
# 1. 后端
cp .env.example .env          # 填入 DEEPSEEK_API_KEY
set -a; source .env; set +a
go mod tidy                   # 首次：拉取 modernc.org/sqlite
go test -race ./...
go run ./cmd/server           # http://127.0.0.1:8080

# 2. 前端（另开终端）
cd web
cp .env.example .env.local
npm ci
npm run dev                   # http://localhost:3000

# 也可以只用命令行
go run ./cmd/cli
```

## 目录

| 路径 | 内容 |
|---|---|
| `internal/llm` | 消息类型、`Model`/`StreamModel` 接口、OpenAI 兼容客户端（SSE、重试） |
| `internal/agent` | `Loop`（无状态循环）、`Agent`（状态、订阅、队列、中断） |
| `internal/tool` | 内置工具 |
| `internal/store` | `Store` 接口、SQLite 与内存实现、`storetest` 契约测试 |
| `internal/server` | HTTP 路由、SSE、事件到 JSON 的转换 |
| `internal/config` | 环境变量配置 |
| `cmd/server`, `cmd/cli` | 程序入口 |
| `web/` | Next.js 前端 |
| `docs/` | [架构](docs/ARCHITECTURE.md)、[API](docs/API.md) |

## 文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：分层、一次请求的完整流程、设计决策、扩展点
- [docs/API.md](docs/API.md)：HTTP 接口与 SSE 事件
- [AGENTS.md](AGENTS.md)：给编码助手（Codex 等）的约定与不变量

## 界面操作

- 左侧新建、切换会话；右上角重命名或确认删除。
- 消息支持 Markdown、折叠思考过程，工具卡片可展开查看参数和结果。
- 空闲时 Enter 发送，Shift + Enter 换行。中文输入法选词不会误发送。
- 运行中可插话、排队或停止。当前连接运行时暂时禁用会话切换，避免意外断流。
- 用户消息旁的“编辑”会填回输入框；“编辑并重发”从该消息的父节点建立分支，旧分支保留在数据库。
- 打开其他连接正在运行的会话时显示运行状态，保留停止/插话/排队，结束后自动刷新历史；不会重放另一个连接的实时流。

## 验证

```bash
gofmt -l cmd internal
go vet ./...
go test -race ./...
cd web
npm test
npm run typecheck
npm run build
npm audit
cd ..
python3 scripts/smoke.py
```

冒烟脚本启动真实 Go 服务、临时 SQLite、生产 Next.js BFF 和本地假流式模型，验证鉴权注入、中文分块、工具、事件结束顺序、重启持久化、分支与会话管理。使用 13000/18080/18081 端口，完成后清理进程和临时数据库，不调用真实模型。加 `--serve` 可保留测试栈用于浏览器检查，Ctrl+C 清理。
