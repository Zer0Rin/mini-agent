# 交付文件映射

来源：`/Users/guyue/Downloads/交付文件/`，44 个原始文件。自动重名后缀按内容恢复，源目录保持原样。

| 来源文件 | 仓库路径 |
|---|---|
| `AGENTS.md` | `AGENTS.md` |
| `API.md` | `docs/API.md` |
| `ARCHITECTURE.md` | `docs/ARCHITECTURE.md` |
| `README.md` | `README.md` |
| `agent.go` | `internal/agent/agent.go` |
| `agent_test.go` | `internal/agent/agent_test.go` |
| `api.ts` | `web/lib/api.ts` |
| `chat-state.ts` | `web/lib/chat-state.ts` |
| `config.go` | `internal/config/config.go` |
| `contract.go` | `internal/store/storetest/contract.go` |
| `datetime.go` | `internal/tool/datetime.go` |
| `datetime_test.go` | `internal/tool/datetime_test.go` |
| `env.example` | `.env.example` |
| `env.example-2` | `web/.env.example` |
| `event.go` | `internal/agent/event.go` |
| `gitignore` | `.gitignore` |
| `go.mod` | `go.mod` |
| `layout.tsx` | `web/app/layout.tsx` |
| `loop.go` | `internal/agent/loop.go` |
| `loop_ext_test.go` | `internal/agent/loop_ext_test.go` |
| `loop_test.go` | `internal/agent/loop_test.go` |
| `main-2.go` | `cmd/server/main.go` |
| `main.go` | `cmd/cli/main.go` |
| `memory.go` | `internal/store/memory.go` |
| `memory_test.go` | `internal/store/memory_test.go` |
| `next.config.ts` | `web/next.config.ts` |
| `openai.go` | `internal/llm/openai.go` |
| `openai_stream_test.go` | `internal/llm/openai_stream_test.go` |
| `openai_test.go` | `internal/llm/openai_test.go` |
| `package.json` | `web/package.json` |
| `page.tsx` | `web/app/page.tsx` |
| `route.ts` | `web/app/api/[...path]/route.ts` |
| `server.go` | `internal/server/server.go` |
| `server_test.go` | `internal/server/server_test.go` |
| `sqlite-2.go` | `internal/store/sqlite.go` |
| `sqlite.go` | `cmd/server/sqlite.go` |
| `sqlite_test.go` | `internal/store/sqlite_test.go` |
| `sse.ts` | `web/lib/sse.ts` |
| `store.go` | `internal/store/store.go` |
| `tool.go` | `internal/agent/tool.go` |
| `tsconfig.json` | `web/tsconfig.json` |
| `types.go` | `internal/llm/types.go` |
| `types.ts` | `web/lib/types.ts` |
| `wire.go` | `internal/server/wire.go` |

恢复后额外修改：README、忽略规则、锁定依赖、前端页面/布局；Next.js 自动更新 tsconfig。核心 Go 代码及原有前端 lib 文件与交付原件一致。
