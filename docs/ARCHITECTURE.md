# 架构

## 分层

依赖只能向下，不能反向：

```
cmd/server, cmd/cli        组装：读配置、选存储、注册工具、启动
        │
internal/server            HTTP/SSE；会话级并发控制；事件 → JSON；消息落库
        │
internal/agent             Agent（有状态外壳）→ Loop（无状态循环）
        │                  ↑ Tool 由 internal/tool 提供
internal/llm               消息类型、Model 接口、OpenAI 兼容客户端
internal/store             Store 接口 + 实现（只依赖 llm 的消息类型）
```

- `agent` 不知道 HTTP 和数据库存在；`store` 不知道 Agent 存在。二者只通过 `llm.Message` 和 `agent.Event` 交汇，交汇点在 `server`。
- `store/sqlite.go` 只依赖 `database/sql`，具体驱动（`modernc.org/sqlite`，纯 Go、无需 CGO）在 `cmd/server/sqlite.go` 里导入。换驱动或换数据库不碰业务代码。

## Harness：两层结构（对应 pi-agent-core）

**`agent.Loop(ctx, cfg, history, prompts...)`**：无状态。给定配置和历史，跑完一次运行，返回新产生的消息。所有行为都由 `Config` 注入：

| Config 字段 | 作用 |
|---|---|
| `Model` | 实现 `StreamModel` 时自动流式，否则调用 `Complete` |
| `Tools` | 允许名单。模型调用不在名单里的工具会得到错误结果，不会执行 |
| `ToolExecution` | 默认并行；任一工具 `Sequential: true` 则整批顺序执行 |
| `TransformContext` | 每次调用模型前改写上下文（裁剪、压缩、注入检索结果）。**只影响发给模型的内容，不影响保存的历史** |
| `BeforeToolCall` | 可拦截（权限、确认、限流），拦截原因作为错误结果回给模型 |
| `AfterToolCall` | 改写结果（脱敏、截断） |
| `GetSteeringMessages` | 每批工具执行后检查，有就插入后再问模型 |
| `GetFollowUpMessages` | 模型给出最终回答后检查，有就继续下一轮 |
| `OnEvent` | 同步、按顺序回调。监听器慢会拖慢循环，这是有意的（背压） |

一次循环：`问模型 → 没有工具调用？(查 steering → 查 follow-up → 结束) → 预检并执行工具 → 按模型给出的顺序回填结果 → 全部 Terminate 且无插话？结束 : 继续`。

**`agent.Agent`**：有状态外壳。持有历史、订阅者、steering/follow-up 队列，保证同一时间只有一次运行（`ErrBusy`），提供 `Abort`（取消 ctx）、`WaitForIdle`、`Continue`（从最后一条 user/toolResult 继续）。出错或中断时已产生的消息仍写入历史。

## 一次“发送消息”的完整流程

```
POST /api/sessions/{id}/messages
 1. claim(id)            占位。同一会话已在运行 → 409。先占位再读历史，防止两个请求读到同一个旧 head
 2. store.Path(head)     读出根 → head 的消息链，作为 Agent 的历史
 3. agent.New + Subscribe
      message_end  → store.AppendMessage(parent = 上一条) → 拿到 id 后再推给前端
      其他事件     → 直接转成 JSON 推送
 4. agent.Prompt(user)   阻塞直到运行结束
 5. 出错推 error；总是推 done；release(id)
```

落库使用 `context.WithoutCancel(r.Context())`：浏览器断开会中断 Agent，但已经完成的消息仍然会写完。
每条消息落库成功后 `head_id` 前移，所以中途崩溃时会话停在最后一条完整的消息上。

## 存储

```
sessions(id, user_id, title, head_id, created_at, updated_at)
messages(id, session_id, parent_id, role, data JSON, created_at)
schema_migrations(version)
```

- **消息树**：每条消息指向父消息，会话的 `head_id` 指向“当前位置”。读取历史 = 从 head 沿 `parent_id` 回溯（SQLite 用递归 CTE）。分叉 = 移动 head，旧分支不删除。
- **消息体存 JSON 列**：`llm.Message` 加字段不需要改表；需要按某字段查询时再提出来单独成列。
- **迁移**：`migrations` 是有序的语句列表，按版本号执行并记录到 `schema_migrations`，重复执行是幂等的。**只追加新版本，不修改已发布的版本。**
- **隔离**：所有方法第一个业务参数是 `userID`；查不到和不属于该用户都返回 `store.ErrNotFound`。

### 契约测试

`internal/store/storetest.Run(t, newStore)` 是 Store 行为的唯一规范（增删改查、用户隔离、分支、字段完整、并发追加）。
**新增任何实现，只需在其 `_test.go` 里调用一次 `storetest.Run`，全部通过才算完成。**

## 扩展点

| 方向 | 做法 | 改动范围 |
|---|---|---|
| PostgreSQL | 新建 `store/postgres.go` 实现 `Store`（占位符 `$1`，CTE 写法相同），跑 `storetest.Run`；`cmd/server` 按 `MINI_AGENT_STORE=postgres` 选择 | store、cmd |
| 登录 / 多用户 | 中间件校验会话或 JWT，把用户 ID 放进 `r.Context()`；改 `server.userID(r)` 一个函数 | server |
| 上下文过长 | `TransformContext` 里按 token 估算裁剪或摘要；摘要结果可作为一条特殊消息存库 | agent 配置 |
| 新工具 | `internal/tool/xxx.go` 返回 `agent.Tool`，参数用结构体 + `json.Unmarshal` 解析；在 `cmd/server/main.go` 注册 | tool、cmd |
| 危险工具确认 | `BeforeToolCall` 返回 block；之后可以做成“等待前端确认”的事件 | server |
| MCP / RAG | MCP 工具适配成 `agent.Tool`；检索放在 `TransformContext` 或做成工具 | tool |
| 换模型 | 实现 `llm.StreamModel`；OpenAI 兼容的服务只需改 `DEEPSEEK_BASE_URL` / 模型名 | llm |
| 评测 | 用 `scriptedModel` 风格的假模型回放固定剧本，断言工具调用序列和最终消息 | 测试 |
| 可观测性 | 订阅事件统计每轮耗时、token 用量（`Message.Usage`）、工具错误率 | server |

## 前端（web/）

- **BFF 代理** `app/api/[...path]/route.ts`：浏览器只访问同源 `/api/*`，由 Next.js 服务端转发并注入 Token；SSE 直接透传（不使用 `rewrites`，它会缓冲）。
- **`lib/types.ts`**：与 `wire.go` 一一对应。
- **`lib/sse.ts`**：POST 请求不能用 `EventSource`，所以用 `fetch` + `ReadableStream` 解析。
- **`lib/chat-state.ts`**：把事件折叠成界面状态的纯函数（`useReducer(applyEvent, ...)`），不依赖 React，可单独测试。
- **`lib/api.ts`**：REST 调用和 `sendMessage` 异步迭代器。

建议的界面（由 Codex 实现）：

1. 左侧会话列表：新建、重命名、删除
2. 消息区：Markdown 渲染；reasoning 可折叠；工具调用显示为卡片（名称、参数、运行中/成功/失败、结果）
3. 输入框：空闲时发送；运行中时变为“插话 / 排队”两个动作，外加停止按钮
4. 用户消息上的“编辑”：`setHead(parent_id)` 后重新发送（分支）
5. 打开会话时 `running: true` 的处理：显示“运行中”，提供停止按钮
