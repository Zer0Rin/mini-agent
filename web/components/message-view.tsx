import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { ChatState } from "@/lib/chat-state";
import type { Message } from "@/lib/types";

export function MessageView({
  message,
  state,
  onEdit,
}: {
  message: Message;
  state: ChatState;
  onEdit?: (message: Message) => void;
}) {
  if (message.role === "toolResult") return null;
  const user = message.role === "user";
  return (
    <article
      className={`message ${user ? "user-message" : "assistant-message"}`}
    >
      <div className="message-label">
        {user ? "你" : "mini-agent"}
        {user && onEdit && (
          <button
            className="text-button"
            onClick={() => onEdit(message)}
            disabled={state.running}
          >
            编辑
          </button>
        )}
      </div>
      {message.reasoning && (
        <details className="reasoning">
          <summary>思考过程</summary>
          <div className="markdown">
            <Markdown remarkPlugins={[remarkGfm]}>{message.reasoning}</Markdown>
          </div>
        </details>
      )}
      {message.content && (
        <div className={user ? "user-content" : "markdown"}>
          {user ? (
            message.content
          ) : (
            <Markdown remarkPlugins={[remarkGfm]}>{message.content}</Markdown>
          )}
        </div>
      )}
      {message.tool_calls?.map((call) => {
        const live = state.tools[call.id];
        const saved = state.messages.find(
          (m) => m.role === "toolResult" && m.tool_call_id === call.id,
        );
        const status = saved
          ? saved.is_error
            ? "error"
            : "ok"
          : (live?.status ?? (state.running ? "running" : "incomplete"));
        return (
          <details
            className="tool-card"
            key={call.id}
            open={status === "running"}
          >
            <summary>
              <span className="tool-name">{call.name}</span>
              <span className={`tool-status ${status}`}>
                {
                  {
                    running: "运行中",
                    ok: "成功",
                    error: "失败",
                    incomplete: "未完成",
                  }[status]
                }
              </span>
            </summary>
            <div className="tool-body">
              <small>参数</small>
              <pre>{call.args}</pre>
              <small>结果</small>
              <pre>
                {saved?.content ??
                  live?.result ??
                  (status === "running" ? "等待工具返回…" : "没有已保存的结果")}
              </pre>
            </div>
          </details>
        );
      })}
      {message.usage && (
        <div className="usage">
          输入 {message.usage.input_tokens} · 输出 {message.usage.output_tokens}{" "}
          tokens
        </div>
      )}
    </article>
  );
}
