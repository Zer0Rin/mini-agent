"use client";

import { useEffect, useReducer, useRef, useState } from "react";
import { api, sendMessage } from "@/lib/api";
import { applyEvent, initialChatState } from "@/lib/chat-state";
import type { Message, SessionDetail } from "@/lib/types";
import { unsavedDraft } from "@/lib/recovery";
import { MessageView } from "./message-view";

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function Chat({
  detail,
  onReload,
  onChanged,
  onLock,
  onBranch,
  initialPrompt,
  initialDraft,
  initialNotice,
  blocked,
}: {
  detail: SessionDetail;
  onReload: (detail: SessionDetail, draft?: string, notice?: string) => void;
  onChanged: () => void;
  onLock: (locked: boolean) => void;
  onBranch: (detail: SessionDetail, content: string) => void;
  initialPrompt?: string;
  initialDraft?: string;
  initialNotice?: string;
  blocked: boolean;
}) {
  const [state, dispatch] = useReducer(applyEvent, detail, (d) => ({
    ...initialChatState(d.messages),
    running: d.running,
  }));
  const [input, setInput] = useState(initialPrompt ?? initialDraft ?? "");
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState(initialNotice ?? "");
  const [controlling, setControlling] = useState(false);
  const [unsynced, setUnsynced] = useState(false);
  const controlOperation = useRef(false);
  const recovery = useRef<{
    content: string;
    count: number;
    draft: string;
  } | null>(null);
  const inputRef = useRef(input);
  inputRef.current = input;
  const [editing, setEditing] = useState<Message | null>(null);
  const [ownStream, setOwnStream] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const operation = useRef(false);
  const transcript = useRef<HTMLDivElement>(null);
  const id = detail.session.id;
  const busy = state.running || ownStream;

  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    onLock(ownStream || pending || controlling);
  }, [ownStream, pending, controlling, onLock]);
  useEffect(() => () => onLock(false), [onLock]);
  useEffect(() => {
    const el = transcript.current;
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 250)
      el.scrollTop = el.scrollHeight;
  }, [state]);

  // A run opened from another tab has no stream to subscribe to. Keep its controls
  // available and reload persisted history when the backend reports completion.
  useEffect(() => {
    if (!detail.running || ownStream) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const latest = await api.getSession(id);
        if (!active) return;
        if (!latest.running && !controlOperation.current) {
          onReload(latest, inputRef.current);
          onChanged();
          return;
        }
      } catch (e) {
        if (active) setNotice(`刷新失败：${errorText(e)}`);
      }
      if (active) timer = setTimeout(poll, 1500);
    };
    timer = setTimeout(poll, 1500);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [detail.running, id, ownStream, onReload, onChanged]);

  useEffect(() => {
    if (!initialPrompt) return;
    const timer = setTimeout(() => {
      void send();
    }, 0);
    return () => clearTimeout(timer);
    // Each mounted history version consumes its initial prompt exactly once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function send() {
    const content = input.trim();
    if (
      !content ||
      busy ||
      operation.current ||
      controlOperation.current ||
      blocked ||
      unsynced
    )
      return;
    operation.current = true;
    setPending(true);
    setNotice("");
    const ctrl = new AbortController();
    controller.current = ctrl;
    let completed = false;
    let started = false;
    try {
      if (editing) {
        await api.setHead(id, editing.parent_id ?? "");
        const latest = await api.getSession(id);
        onBranch(latest, content);
        return;
      }
      setInput("");
      setOwnStream(true);
      for await (const event of sendMessage(id, content, ctrl.signal)) {
        if (!started) {
          started = true;
          setPending(false);
        }
        dispatch(event);
        if (event.type === "done") completed = true;
      }
      if (!completed)
        throw new Error("连接提前结束，请检查已保存的消息后重试。");
    } catch (e) {
      ctrl.abort();
      recovery.current = {
        content,
        count: state.messages.length,
        draft: inputRef.current,
      };
      setUnsynced(true);
      setInput((current) => current || content);
      const failure = errorText(e);
      setNotice(failure);
      await reconcile(failure);
    } finally {
      controller.current = null;
      operation.current = false;
      setPending(false);
      setOwnStream(false);
      onChanged();
    }
  }

  async function reconcile(failure = "连接已恢复，已重新读取保存记录。") {
    setPending(true);
    try {
      const latest = await api.getSession(id);
      const r = recovery.current;
      onReload(
        latest,
        r
          ? r.draft || unsavedDraft(latest.messages, r.count, r.content)
          : input,
        failure,
      );
    } catch (e) {
      setNotice(`无法确认保存状态：${errorText(e)}。请重新加载历史后再发送。`);
    } finally {
      setPending(false);
    }
  }

  async function control(action: "abort" | "steer" | "followUp") {
    if (controlOperation.current || blocked || unsynced || pending) return;
    const content = input.trim();
    if (action !== "abort" && !content) return;
    controlOperation.current = true;
    setControlling(true);
    setNotice("");
    // Clear at submission, so text typed while awaiting the response is retained.
    if (action !== "abort") setInput("");
    try {
      if (action === "abort") await api.abort(id);
      else await api[action](id, content);
      setNotice(
        action === "abort"
          ? "已请求停止，正在保存消息。"
          : action === "steer"
            ? "插话已送达。"
            : "已加入队列。",
      );
    } catch (e) {
      if (action !== "abort")
        setInput((current) =>
          current && current !== content ? `${content}\n\n${current}` : content,
        );
      setNotice(errorText(e));
    } finally {
      controlOperation.current = false;
      setControlling(false);
    }
  }

  return (
    <section className="chat" aria-label="对话">
      <header className="chat-header">
        <div>
          <span className="eyebrow">对话</span>
          <h1>{detail.session.title || "新会话"}</h1>
        </div>
        <span className={`run-state ${busy ? "active" : ""}`} role="status">
          {busy ? "运行中" : "就绪"}
        </span>
      </header>
      <div
        className="transcript"
        ref={transcript}
        aria-label="消息记录"
        aria-busy={busy}
      >
        {!state.messages.length && !busy && (
          <div className="welcome">
            <div className="welcome-mark">
              m<span>·</span>
            </div>
            <h2>从一个问题开始</h2>
            <p>把想法写下来，和 mini-agent 一起推进。</p>
          </div>
        )}
        <div className="messages">
          {state.messages.map((m, i) => (
            <MessageView
              key={m.id ?? i}
              message={m}
              state={state}
              onEdit={
                busy || controlling || blocked || unsynced
                  ? undefined
                  : (message) => {
                      setEditing(message);
                      setInput(message.content);
                    }
              }
            />
          ))}
          {state.draft && (
            <MessageView
              message={{ role: "assistant", ...state.draft }}
              state={state}
            />
          )}
          {busy && (
            <p className="thinking" role="status">
              {detail.running && !ownStream
                ? "此会话正在其他连接中运行，完成后会自动刷新。"
                : "正在处理…"}
            </p>
          )}
        </div>
      </div>
      <div className="composer-wrap">
        {(state.error || notice) && (
          <p className="notice" role="status">
            {notice || state.error}
          </p>
        )}
        {unsynced && (
          <button
            disabled={pending || blocked}
            onClick={() => void reconcile()}
          >
            重新加载历史
          </button>
        )}
        {editing && (
          <div className="edit-banner">
            编辑后将从这条消息建立分支，保留原分支。
            <button
              onClick={() => {
                setEditing(null);
                setInput("");
              }}
            >
              取消编辑
            </button>
          </div>
        )}
        <form
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <label className="sr-only" htmlFor="message">
            消息
          </label>
          <textarea
            id="message"
            disabled={blocked || unsynced}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={busy ? "继续补充，选择插话或排队…" : "输入消息…"}
            rows={3}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing &&
                !busy
              ) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <div className="composer-actions">
            <span>
              {busy
                ? "插话在工具结束后送达；排队在回答结束后送达"
                : "Enter 发送 · Shift + Enter 换行"}
            </span>
            <div className="buttons">
              {busy ? (
                <>
                  <button
                    type="button"
                    disabled={pending || controlling || blocked || unsynced}
                    onClick={() => void control("abort")}
                  >
                    停止
                  </button>
                  <button
                    type="button"
                    disabled={
                      pending ||
                      controlling ||
                      blocked ||
                      unsynced ||
                      !input.trim()
                    }
                    onClick={() => void control("steer")}
                  >
                    插话
                  </button>
                  <button
                    className="primary"
                    type="button"
                    disabled={
                      pending ||
                      controlling ||
                      blocked ||
                      unsynced ||
                      !input.trim()
                    }
                    onClick={() => void control("followUp")}
                  >
                    排队
                  </button>
                </>
              ) : (
                <button
                  className="primary"
                  disabled={
                    pending ||
                    controlling ||
                    blocked ||
                    unsynced ||
                    !input.trim()
                  }
                >
                  {pending ? "发送中…" : editing ? "编辑并重发" : "发送 ↑"}
                </button>
              )}
            </div>
          </div>
        </form>
      </div>
    </section>
  );
}
