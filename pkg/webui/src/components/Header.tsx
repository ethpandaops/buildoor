import React from 'react';
import { BrandHeader } from './BrandHeader';
import { HeaderNav } from './HeaderNav';
import { UserDisplay } from './UserDisplay';
import { setView } from '../stores/viewStore';

export const Header: React.FC = () => {
  return (
    <BrandHeader
      title="Buildoor"
      onBrandClick={() => setView('dashboard')}
      navItems={<HeaderNav />}
      endContent={<UserDisplay />}
    />
  );
};
