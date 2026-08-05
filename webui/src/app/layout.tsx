import type { Metadata } from 'next';

import './globals.css';

export const metadata: Metadata = {
  title: 'video-stream',
  description: 'Local-first AI video production pipeline',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN">
      <body className="min-h-screen">{children}</body>
    </html>
  );
}
