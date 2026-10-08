# HTTP API

所有 `/api/*` 接口：

- 请求和响应都是 JSON；请求体上限 1 MB。
- 设置了 `MINI_AGENT_TOKEN` 时需要 `Authorization: Bearer <token>`，否则返回 `401`。
- 错误统一为 `{"error": "..."}`。会话不存在或不属于当前用户都返回 `404`（不区分，防止探测）。
- 类型定义见 `web/lib/types.ts`，与 `internal/server/wire.go` 一一对应。

## 会话

| 方法 | 路径 | 请求体 | 成功响应 |
|---|---|---|---|
| GET | `/healthz` | | `200 ok`（不鉴权） |
| GET | `/api/sessions` | | `200 Session[]`，按更新时间倒序 |
| POST | `/api/sessions` | `{title?}` | `201 Session` |
| GET | `/api/sessions/{id}` | | `200 {session, messages, running}`；`messages` 是从根到 `head_id` 的路径 |
| PATCH | `/api/sessions/{id}` | `{title}` | `204` |
| DELETE | `/api/sessions/{id}` | | `204`；运行中返回 `409` |
| POST | `/api/sessions/{id}/head` | `{message_id}` | `204`；把 head 移到某条消息，`""` 表示回到开头。运行中返回 `409` |

**分支**：head 移到消息 X 后再发送消息，新消息的 `parent_id` 就是 X，原来 X 之后的消息仍保留在库里（成为另一条分支）。
“编辑并重发第 N 条用户消息” = 把 head 设到这条消息的 `parent_id`，再发送新内容。

## 发送消息（SSE）

`POST /api/sessions/{id}/messages`，请求体 `{content}`。

- 会话已有运行中的任务：`409`（改用 steer / follow-up）。
- 否则返回 `200`，`Content-Type: text/event-stream`，每个事件为：

```
event: <type>
data: <AgentEvent JSON>

```

客户端断开连接 = 中断本次运行。已经 `message_end` 的消息都已落库。

### 事件顺序

一次只回答文本的运行：

```
agent_start
message_start(user) → message_end(user, 带 id)
turn_start
message_start(assistant) → message_update × N → message_end(assistant, 带 id)
turn_end
agent_end
done
```

带工具调用时，assistant 的 `message_end` 之后是：

```
tool_execution_start × K  →  tool_execution_end × K（并行时按完成顺序）
message_start(toolResult) → message_end(toolResult)  × K（按模型给出的顺序）
turn_end
turn_start → 下一次模型调用 ...
```

出错时在 `done` 之前多一个 `error` 事件。**`done` 永远是最后一个事件**，前端以它为准结束“运行中”状态。

### 事件字段

| type | 字段 |
|---|---|
| `message_start` / `message_end` | `message`（`message_end` 时带 `id`、`parent_id`、`created_at`） |
| `message_update` | `delta: {content?, reasoning?}` |
| `tool_execution_start` | `tool_call_id`, `tool_name`, `args`（原始 JSON 字符串） |
| `tool_execution_update` | `tool_call_id`, `tool_name`, `result`（中间进度） |
| `tool_execution_end` | `tool_call_id`, `tool_name`, `result: {content, is_error}` |
| `error` | `error`：`aborted` / `max turns exceeded` / `model api error (status N)` / `agent error` |
| 其他 | 无额外字段 |

## 运行中控制

| 方法 | 路径 | 请求体 | 说明 |
|---|---|---|---|
| POST | `/api/sessions/{id}/abort` | | `202`；没有运行中任务时 `409` |
| POST | `/api/sessions/{id}/steer` | `{content}` | `202`；在当前工具执行完、下一次调用模型前插入 |
| POST | `/api/sessions/{id}/follow-up` | `{content}` | `202`；当前回答结束后作为新问题继续处理 |

steer / follow-up 产生的用户消息同样通过原来的 SSE 连接以 `message_start/end` 推送。
