import type { ReactNode } from "react";
import "./globals.css";

export const metadata = { title: "mini-agent", description: "你的思考工作台" };

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
