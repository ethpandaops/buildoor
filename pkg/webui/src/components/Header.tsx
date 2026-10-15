import React from 'react';
import { BrandHeader } from './BrandHeader';
import { HeaderNav } from './HeaderNav';
import { UserDisplay } from './UserDisplay';
import { LiveStatus } from './LiveStatus';
import { setView } from '../stores/viewStore';
import '../styles/header.css';

// Header is the app bar: brand, the Overview/Operator switch with the
// operator pages, the live stream status and the login.
export const Header: React.FC = () => {
  return (
    <BrandHeader
      title="Buildoor"
      brandHref="/"
      onBrandClick={() => setView('overview')}
      navItems={<HeaderNav />}
      endContent={
        <span className="d-flex align-items-center gap-3">
          <LiveStatus />
          <UserDisplay />
        </span>
      }
    />
  );
};
