// 把浏览器的 /api/* 请求转发给 Go 后端（BFF 代理）。
//
// 为什么不用 next.config 的 rewrites：
//   1. 这里可以在服务端注入 MINI_AGENT_TOKEN，Token 不会出现在浏览器里；
//   2. 直接把上游的 body 流式透传，SSE 不会被缓冲；
//   3. 浏览器断开时通过 req.signal 取消上游请求，后端随之中断 Agent。

export const dynamic = "force-dynamic";

const BACKEND_URL = process.env.BACKEND_URL ?? "http://127.0.0.1:8080";
const TOKEN = process.env.MINI_AGENT_TOKEN ?? "";

async function proxy(req: Request): Promise<Response> {
  const url = new URL(req.url);
  const target = BACKEND_URL + url.pathname + url.search;

  const headers = new Headers({ "Content-Type": req.headers.get("Content-Type") ?? "application/json" });
  if (TOKEN) headers.set("Authorization", `Bearer ${TOKEN}`);

  const hasBody = req.method !== "GET" && req.method !== "HEAD";
  const upstream = await fetch(target, {
    method: req.method,
    headers,
    body: hasBody ? await req.text() : undefined,
    signal: req.signal,
    cache: "no-store",
  });

  const out = new Headers();
  for (const key of ["Content-Type", "Cache-Control"]) {
    const v = upstream.headers.get(key);
    if (v) out.set(key, v);
  }
  if (out.get("Content-Type")?.startsWith("text/event-stream")) {
    out.set("X-Accel-Buffering", "no");
  }
  return new Response(upstream.body, { status: upstream.status, headers: out });
}

export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
