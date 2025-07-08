import { docs } from '@/.source';
import { loader } from 'fumadocs-core/source';
import { i18n } from '@/lib/i18n';
 
export const source = loader({
  i18n,
  baseUrl: '/docs',
  source: docs.toFumadocsSource(),
});
