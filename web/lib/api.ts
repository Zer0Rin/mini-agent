import { parseSSE } from "./sse";
import type { AgentEvent, Session, SessionDetail } from "./types";

// 浏览器只访问同源的 /api/*，由 Next.js 的路由处理器转发给 Go 后端（见 app/api/[...path]/route.ts）。

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body.error ?? res.statusText);
  }
  // 204 / 202（abort、steer 等）没有响应体
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export const api = {
  listSessions: () => request<Session[]>("/api/sessions"),
  createSession: (title = "") => request<Session>("/api/sessions", { method: "POST", body: JSON.stringify({ title }) }),
  getSession: (id: string) => request<SessionDetail>(`/api/sessions/${id}`),
  renameSession: (id: string, title: string) =>
    request<void>(`/api/sessions/${id}`, { method: "PATCH", body: JSON.stringify({ title }) }),
  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: "DELETE" }),
  /** 分叉：把会话位置移到某条历史消息（空字符串 = 回到开头） */
  setHead: (id: string, messageId: string) =>
    request<void>(`/api/sessions/${id}/head`, { method: "POST", body: JSON.stringify({ message_id: messageId }) }),
  abort: (id: string) => request<void>(`/api/sessions/${id}/abort`, { method: "POST" }),
  steer: (id: string, content: string) =>
    request<void>(`/api/sessions/${id}/steer`, { method: "POST", body: JSON.stringify({ content }) }),
  followUp: (id: string, content: string) =>
    request<void>(`/api/sessions/${id}/follow-up`, { method: "POST", body: JSON.stringify({ content }) }),
};

/**
 * 发送消息并逐个产出事件，直到 done。
 * 传入的 signal 被取消时连接断开，后端会中断这次运行（已完成的消息仍会保存）。
 */
export async function* sendMessage(id: string, content: string, signal?: AbortSignal): AsyncGenerator<AgentEvent> {
  const res = await fetch(`/api/sessions/${id}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content }),
    signal,
  });
  if (!res.ok || !res.body) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body.error ?? res.statusText);
  }
  yield* parseSSE(res.body);
}
