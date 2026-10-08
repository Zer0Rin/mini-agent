import type { NextConfig } from "next";

// 不使用 rewrites 代理 /api：SSE 会被缓冲，且无法在服务端注入 Token。
// 转发逻辑在 app/api/[...path]/route.ts。
const nextConfig: NextConfig = {};

export default nextConfig;
