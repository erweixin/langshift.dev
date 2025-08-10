import '@/styles/global.css';
import Script from 'next/script';
import { defaultStructuredData, organizationStructuredData } from '@/lib/seo-structured-data';
import { Analytics } from '@/components/analytics';

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        {/* Pyodide 运行时 */}
        <Script
          src="https://cdn.jsdelivr.net/pyodide/v0.27.0/full/pyodide.js"
          strategy="beforeInteractive"
        />
        
        {/* 基础图标和 PWA 支持 */}
        <link rel="icon" href="/favicon.ico" />
        <link rel="manifest" href="/manifest.webmanifest" />
        
        {/* 主题和颜色 */}
        <meta name="theme-color" content="#1e293b" />
        <meta name="color-scheme" content="light dark" />
        
        {/* 结构化数据 */}
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: JSON.stringify(defaultStructuredData)
          }}
        />
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: JSON.stringify(organizationStructuredData)
          }}
        />
      </head>
      <body>
        {children}
        <Analytics />
      </body>
    </html>
  );
} 