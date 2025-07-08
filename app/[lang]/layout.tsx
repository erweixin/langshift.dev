import '@/styles/global.css';

import { I18nProvider, type Translations } from 'fumadocs-ui/i18n';
import { RootProvider } from 'fumadocs-ui/provider';
import Script from 'next/script';

const zhCn: Partial<Translations> = {
  search: '簡體中文',
  // other translations
};

const zhTw: Partial<Translations> = {
  search: '繁體中文',
  // other translations
};

// available languages that will be displayed on UI
// make sure `locale` is consistent with your i18n config
const locales = [
  {
    name: 'English',
    locale: 'en',
  },
  {
    name: 'Simplified Chinese',
    locale: 'zh-cn',
  },
  {
    name: 'Traditional Chinese',
    locale: 'zh-tw',
  },
];

export default async function RootLayout({
  params,
  children,
}: {
  params: Promise<{ lang: string }>;
  children: React.ReactNode;
}) {
  const lang = (await params).lang;

  return (
    <html lang={lang} suppressHydrationWarning>
           <head>
        <Script
          src="https://cdn.jsdelivr.net/pyodide/v0.27.0/full/pyodide.js"
          strategy="beforeInteractive"
        />
        <link rel="icon" href="/favicon.ico" />
        <link rel="apple-touch-icon" href="/apple-touch-icon.png" />
        <link rel="manifest" href="/manifest.json" />
        <meta name="theme-color" content="#1e293b" />
        <meta name="color-scheme" content="light dark" />
      </head>
      <body>
        <I18nProvider
          locale={lang}
          locales={locales}
          translations={{ zhCn, zhTw }[lang]}
        >
          <RootProvider>{children}</RootProvider>
        </I18nProvider>
      </body>
    </html>
  );
}
