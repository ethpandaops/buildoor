import React from 'react';
import { getViewPath, setView, useView, type ViewType } from '../stores/viewStore';
import { getRuntimeConfig } from '../utils/runtimeConfig';
import { useAuthContext } from '../context/AuthContext';
import { closeMobileNav } from './BrandHeader';

// Items always visible. `audit-log` is added only for authenticated users.
const NAV_ITEMS: Array<{ view: ViewType; label: string; requiresAuth?: boolean }> = [
  { view: 'dashboard', label: 'Dashboard' },
  { view: 'bids-won', label: 'Bids Won' },
  { view: 'validators', label: 'Validators' },
  { view: 'proposer-preferences', label: 'Proposer Prefs' },
  { view: 'builder-preferences', label: 'Builder Prefs' },
  { view: 'audit-log', label: 'Audit Log', requiresAuth: true },
  { view: 'api-docs', label: 'API' },
];

export const HeaderNav: React.FC = () => {
  const currentView = useView();
  const { overviewURL } = getRuntimeConfig();
  const { isLoggedIn } = useAuthContext();

  const navItems = NAV_ITEMS.filter((item) => !item.requiresAuth || isLoggedIn);

  return (
    <>
      {overviewURL && (
        <li className="nav-item px-2">
          <a href={overviewURL} className="nav-link header-nav-button">
            <span className="nav-text">
              Overview <i className="fas fa-arrow-up-right-from-square ms-1 small"></i>
            </span>
          </a>
        </li>
      )}
      {navItems.map((item) => (
        <li key={item.view} className="nav-item px-2">
          <a
            href={getViewPath(item.view)}
            className={`nav-link header-nav-button ${currentView === item.view ? 'active' : ''}`}
            onClick={(e) => {
              e.preventDefault();
              closeMobileNav();
              setView(item.view);
            }}
          >
            <span className="nav-text">{item.label}</span>
          </a>
        </li>
      ))}
    </>
  );
};
