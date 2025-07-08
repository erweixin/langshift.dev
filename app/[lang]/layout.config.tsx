import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';

import { Logo } from '@/components/logo';

export const baseOptions: BaseLayoutProps = {
  i18n: true,
  nav: {
    title: <Logo />,
  },
};
