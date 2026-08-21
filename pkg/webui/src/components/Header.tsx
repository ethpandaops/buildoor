import React from 'react';
import { BrandHeader } from './BrandHeader';
import { HeaderNav } from './HeaderNav';
import { UserDisplay } from './UserDisplay';
import { setView } from '../stores/viewStore';
import { getRuntimeConfig } from '../utils/runtimeConfig';

export const Header: React.FC = () => {
  // When an overview UI is configured, the brand link leaves this instance
  // for the multi-instance overview; otherwise it stays a dashboard shortcut.
  const { overviewURL } = getRuntimeConfig();

  return (
    <BrandHeader
      title="Buildoor"
      brandHref={overviewURL || '/'}
      onBrandClick={overviewURL ? undefined : () => setView('dashboard')}
      navItems={<HeaderNav />}
      endContent={<UserDisplay />}
    />
  );
};
