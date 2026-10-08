// 与 Go 后端 internal/server/wire.go 一一对应。修改任何一边都要同步修改另一边和 docs/API.md。

export type Role = "user" | "assistant" | "toolResult";

export interface Session {
  id: string;
  title: string;
  head_id: string;
  created_at: string;
  updated_at: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args: string; // 模型给的原始 JSON 字符串，不保证合法
}

export interface Usage {
  input_tokens: number;
  output_tokens: number;
}

export interface Message {
  id?: string; // 已保存的消息才有
  parent_id?: string;
  role: Role;
  content: string;
  reasoning?: string;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  tool_name?: string;
  is_error?: boolean;
  usage?: Usage;
  created_at?: string;
}

export interface SessionDetail {
  session: Session;
  messages: Message[];
  running: boolean;
}

export type EventType =
  | "agent_start"
  | "agent_end"
  | "turn_start"
  | "turn_end"
  | "message_start"
  | "message_update"
  | "message_end"
  | "tool_execution_start"
  | "tool_execution_update"
  | "tool_execution_end"
  | "error"
  | "done";

export interface AgentEvent {
  type: EventType;
  message?: Message;
  delta?: { content?: string; reasoning?: string };
  tool_call_id?: string;
  tool_name?: string;
  args?: string;
  result?: { content: string; is_error: boolean };
  error?: string;
}
