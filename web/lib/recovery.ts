import type { Message } from "./types";

// A saved prompt may have reached SQLite even when its SSE frame was lost.
// Only inspect messages appended after this request's known history.
export function unsavedDraft(
  messages: Message[],
  previousCount: number,
  submitted: string,
): string {
  const saved = messages
    .slice(previousCount)
    .some((m) => m.role === "user" && m.content === submitted);
  return saved ? "" : submitted;
}
