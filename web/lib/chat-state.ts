import type { AgentEvent, Message } from "./types";

// 把 SSE 事件折叠成界面状态。纯函数、不依赖 React，方便单独测试；
// 在组件里用 useReducer(applyEvent, initialChatState(messages)) 即可。

export interface ToolRun {
  id: string;
  name: string;
  args: string;
  status: "running" | "ok" | "error";
  result?: string;
}

export interface ChatState {
  messages: Message[]; // 已保存的消息（都带 id）
  draft: { content: string; reasoning: string } | null; // 正在流式输出的 assistant 消息
  tools: Record<string, ToolRun>; // 按 tool_call_id 索引，用于展示调用过程
  running: boolean;
  error: string | null;
}

export function initialChatState(messages: Message[] = []): ChatState {
  return { messages, draft: null, tools: {}, running: false, error: null };
}

export function applyEvent(s: ChatState, e: AgentEvent): ChatState {
  switch (e.type) {
    case "agent_start":
      return { ...s, running: true, error: null, tools: {} };

    case "message_start":
      return e.message?.role === "assistant" ? { ...s, draft: { content: "", reasoning: "" } } : s;

    case "message_update":
      if (!s.draft || !e.delta) return s;
      return {
        ...s,
        draft: {
          content: s.draft.content + (e.delta.content ?? ""),
          reasoning: s.draft.reasoning + (e.delta.reasoning ?? ""),
        },
      };

    case "message_end":
      if (!e.message) return s;
      return {
        ...s,
        messages: [...s.messages, e.message],
        draft: e.message.role === "assistant" ? null : s.draft,
      };

    case "tool_execution_start":
      if (!e.tool_call_id) return s;
      return {
        ...s,
        tools: {
          ...s.tools,
          [e.tool_call_id]: { id: e.tool_call_id, name: e.tool_name ?? "", args: e.args ?? "", status: "running" },
        },
      };

    case "tool_execution_end": {
      const id = e.tool_call_id;
      if (!id || !s.tools[id]) return s;
      return {
        ...s,
        tools: {
          ...s.tools,
          [id]: { ...s.tools[id], status: e.result?.is_error ? "error" : "ok", result: e.result?.content },
        },
      };
    }

    case "error":
      return { ...s, error: e.error ?? "unknown error" };

    case "done":
      return { ...s, running: false, draft: null };

    default:
      return s;
  }
}
