"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Session, SessionDetail } from "@/lib/types";
import { Chat } from "@/components/chat";

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export default function Home() {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [view, setView] = useState<{
    detail: SessionDetail;
    revision: number;
    prompt?: string;
    draft?: string;
    notice?: string;
  } | null>(null);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [locked, setLocked] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [rename, setRename] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const requestVersion = useRef(0);
  const busy = pending || locked;

  const refresh = useCallback(async () => {
    try {
      setSessions(await api.listSessions());
      setConnected(true);
      setError("");
    } catch (e) {
      setConnected(false);
      setError(errorText(e));
    }
  }, []);
  const changed = useCallback(() => {
    void refresh();
  }, [refresh]);
  const reload = useCallback(
    (detail: SessionDetail, draft?: string, notice?: string) => {
      setView((v) => ({
        detail,
        revision: (v?.revision ?? 0) + 1,
        draft,
        notice,
      }));
    },
    [],
  );
  const branch = useCallback((detail: SessionDetail, prompt: string) => {
    setView((v) => ({ detail, revision: (v?.revision ?? 0) + 1, prompt }));
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function select(id: string) {
    const version = ++requestVersion.current;
    setPending(true);
    setError("");
    setRename(null);
    setDeleting(false);
    try {
      const detail = await api.getSession(id);
      if (version === requestVersion.current) {
        reload(detail);
        setMenuOpen(false);
      }
    } catch (e) {
      if (version === requestVersion.current) setError(errorText(e));
    } finally {
      if (version === requestVersion.current) setPending(false);
    }
  }
  async function create() {
    setPending(true);
    setError("");
    try {
      const session = await api.createSession("新会话");
      reload({ session, messages: [], running: false });
      setMenuOpen(false);
      await refresh();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  }
  async function saveTitle() {
    if (!view || !rename?.trim()) return;
    setPending(true);
    setError("");
    try {
      await api.renameSession(view.detail.session.id, rename.trim());
      setView(
        (v) =>
          v && {
            ...v,
            detail: {
              ...v.detail,
              session: { ...v.detail.session, title: rename.trim() },
            },
          },
      );
      setRename(null);
      await refresh();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  }
  async function remove() {
    if (!view) return;
    setPending(true);
    setError("");
    try {
      await api.deleteSession(view.detail.session.id);
      setView(null);
      setDeleting(false);
      await refresh();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="app-shell">
      <button
        className="mobile-menu"
        aria-expanded={menuOpen}
        aria-controls="sidebar"
        onClick={() => setMenuOpen(!menuOpen)}
      >
        {menuOpen ? "关闭列表" : "会话列表"}
      </button>
      <aside id="sidebar" className={`sidebar ${menuOpen ? "open" : ""}`}>
        <div className="brand">
          <span className="brand-symbol">m·</span>
          <div>
            mini-agent<small>你的思考工作台</small>
          </div>
        </div>
        <button
          className="new-session"
          disabled={busy || !connected}
          onClick={() => void create()}
        >
          <span>＋</span> 新建会话
        </button>
        <div className="sidebar-heading">
          会话 <span>{sessions.length}</span>
        </div>
        <nav aria-label="会话列表">
          {sessions.map((s) => (
            <button
              key={s.id}
              className={`session-item ${view?.detail.session.id === s.id ? "selected" : ""}`}
              disabled={busy}
              onClick={() => void select(s.id)}
            >
              <span>{s.title || "未命名会话"}</span>
              <small>
                {new Date(s.updated_at).toLocaleDateString("zh-CN", {
                  month: "short",
                  day: "numeric",
                })}
              </small>
            </button>
          ))}
        </nav>
        {connected && !sessions.length && (
          <p className="empty-list">
            还没有会话。
            <br />
            新建一个，开始对话。
          </p>
        )}
        <div className="connection">
          <span className={`connection-dot ${connected ? "online" : ""}`} />
          <span>
            {connected
              ? `已连接后端，共 ${sessions.length} 个会话`
              : "后端未连接"}
          </span>
        </div>
      </aside>
      <div className="workspace">
        {error && (
          <div className="error-banner" role="alert">
            {error}
            <button onClick={() => void refresh()}>重新连接</button>
          </div>
        )}
        {view && (
          <div className="session-toolbar">
            {rename !== null ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void saveTitle();
                }}
              >
                <input
                  aria-label="会话名称"
                  autoFocus
                  value={rename}
                  onChange={(e) => setRename(e.target.value)}
                  maxLength={200}
                />
                <button disabled={busy || !rename.trim()}>保存名称</button>
                <button type="button" onClick={() => setRename(null)}>
                  取消
                </button>
              </form>
            ) : (
              <button
                disabled={busy}
                onClick={() => setRename(view.detail.session.title)}
              >
                重命名
              </button>
            )}
            {deleting ? (
              <div>
                <span>删除这个会话及全部消息？</span>
                <button
                  className="danger"
                  disabled={busy}
                  onClick={() => void remove()}
                >
                  确认删除
                </button>
                <button onClick={() => setDeleting(false)}>取消</button>
              </div>
            ) : (
              <button
                disabled={busy || view.detail.running}
                onClick={() => setDeleting(true)}
              >
                删除会话
              </button>
            )}
          </div>
        )}
        {view ? (
          <Chat
            key={`${view.detail.session.id}:${view.revision}`}
            detail={view.detail}
            initialPrompt={view.prompt}
            initialDraft={view.draft}
            initialNotice={view.notice}
            blocked={pending}
            onReload={reload}
            onBranch={branch}
            onChanged={changed}
            onLock={setLocked}
          />
        ) : (
          <section className="landing">
            <span className="eyebrow">MINI-AGENT / 工作台</span>
            <div className="welcome-mark">
              m<span>·</span>
            </div>
            <h1>
              留一个空间，
              <br />
              给下一个想法。
            </h1>
            <p>连续对话、调用工具，或从一条消息重新出发。</p>
            <button
              className="primary"
              disabled={busy || !connected}
              onClick={() => void create()}
            >
              开始新会话 ＋
            </button>
          </section>
        )}
      </div>
    </main>
  );
}
