import { createI18nMiddleware } from 'fumadocs-core/i18n';
import { i18n } from '@/lib/i18n';

export default createI18nMiddleware(i18n);

export const config = {
  // 排除根路径 `/`，只匹配其它路径
  matcher: ['/((?!api|_next/static|_next/image|favicon.ico|$|sitemap.xml|robots.txt).*)'],
};