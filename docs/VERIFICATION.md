# 本机验收记录

日期：2026-10-08（Asia/Taipei）。环境：macOS arm64，Go 1.26.5，Node 24.14.1，npm 11.11.0。

## 通过的检查

- 44 份交付文件路径完整；核心 Go 代码、`api.ts`、`sse.ts`、`chat-state.ts`、`types.ts` 与原件一致。
- `gofmt -l cmd internal` 无输出，`go vet ./...`、`go test -race ./...` 通过。
- SQLite 契约的会话 CRUD、用户隔离、消息树/分支、字段完整保存、并发追加，以及重复迁移通过；同一套 Memory 契约通过。
- `npm test`：逐字节分割 UTF-8 SSE、独立并行工具结果、已保存/未保存输入恢复，3 项通过。
- `npm run typecheck`、`npm run build` 通过；`npm audit` 报告 0 个漏洞。
- `python3 scripts/smoke.py`：真实 Go + 临时 SQLite + 生产 Next BFF + 本地假流式模型，验证 Token 服务端注入、中文 SSE、工具、done-last、服务重启后的持久化、分支和会话 CRUD。
- Chromium 浏览器：新建、发送中文、Markdown、工具和折叠思考、编辑并重发、重命名、确认删除、插话、排队、停止；390px 手机与 1440px 桌面视口检查。
- 延迟控制请求时按钮保持禁用，期间新输入的草稿保留；延迟切换会话时旧 composer 禁用。
- 模型消息已保存但下游 SSE 帧缺失时恢复历史，不恢复已保存的输入供重复发送。
- 发送失败且详情查询也失败时禁止继续发送；详情恢复后保留未保存的草稿。
- 从另一连接打开运行中的会话显示运行状态，可停止；完成轮询后保留当前草稿。
- 独立代码复审发现的异步边界问题已修复并复审。

## 依赖与运行边界

SQLite 固定 `modernc.org/sqlite v1.38.2` 以保留 Go 1.24.7 最低版本；本次实际运行使用 Go 1.26.5，未额外下载 Go 1.24.7 验证。前端依赖锁定在 `web/package-lock.json`（Next 16.4.0 / React 19.3.0）。

已创建忽略 Git 的 `.env` 与 `web/.env.local` 示例配置。真实 DeepSeek 请求尚未执行，需要用户填入有效 API Key。测试使用本地假模型，测试栈和临时数据库在完成后清理。

浏览器截图保存在忽略 Git 的 `output/playwright/desktop.png`、`output/playwright/mobile.png`。完整复跑命令见 README。
