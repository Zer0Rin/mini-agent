# AGENTS.md — 给编码助手的约定

本仓库的核心（`internal/agent`、`internal/llm`、`internal/store`、`internal/server`）已经过审查和测试。
扩展功能时优先**新增**代码，修改核心前先读 `docs/ARCHITECTURE.md`。

## 必须保持的不变量

1. **分层**：`agent` 不导入 `server`/`store`；`store` 不导入 `agent`；SQL 驱动只在 `cmd/` 导入。
2. **用户隔离**：`Store` 的每个方法都带 `userID`；不存在和无权访问一律返回 `store.ErrNotFound`。新的查询必须带 `user_id` 条件或先校验会话归属。
3. **先占位再读历史**：`server.sendMessage` 中 `claim` 必须在 `Store.Path` 之前。
4. **落库用 `context.WithoutCancel`**：断开连接不能丢掉已完成的消息。
5. **`done` 是最后一个 SSE 事件**，任何路径（包括出错）都要发送。
6. **工具结果按模型给出的顺序回填**，即使并行执行时完成顺序不同。
7. **迁移只追加**：不改已有版本的 SQL。
8. **`web/lib/types.ts` ↔ `internal/server/wire.go` ↔ `docs/API.md`** 三者同步修改。
9. **密钥只从环境变量读取**。不写进代码、测试、日志或提交；前端不使用 `NEXT_PUBLIC_` 前缀暴露 Token。

## 代码规则（来自之前的代码审查）

- **模型输出不可信**：工具名查允许名单，参数用结构体解析并校验，解析失败返回错误结果给模型，不 panic。
- **不静默失败**：不要用 `_` 丢掉 error；不能处理就返回，或至少记录日志并说明原因。
- **算出来的信息要传到位**：计算了某个值（如错误原因、用量）就要传给调用方或事件，不要中途丢弃。
- **注释与代码一致**：改代码时同步改注释；发现不一致视为 bug。
- **每个分支都有测试**：新增的 if/switch 分支要有对应用例，优先表格驱动。

## 测试

```bash
gofmt -l .                 # 应无输出
go vet ./...
go test -race ./...
cd web && npm run typecheck && npm run build
```

- Agent 相关：用 `scriptedModel`（见 `internal/agent/loop_test.go`）按剧本返回，不要调用真实模型。
- LLM 客户端：用 `httptest.Server` 模拟接口。
- 存储：新实现必须通过 `storetest.Run`。
- 服务端：`internal/server/server_test.go` 有内存存储 + 假模型的完整流程示例。
- 涉及计时的测试留足余量，`-race` 下会变慢。

## 工作方式

- 每次改动保持小而完整：一个功能 = 代码 + 测试 + 文档更新。
- 改动 API 时先改 `docs/API.md`，再改两端。
- 不确定的设计先在 PR 描述里写出取舍，不要直接修改不变量。
