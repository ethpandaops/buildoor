import React from 'react';
import { getViewPath, setView, useView, type ViewType } from '../stores/viewStore';
import { getRuntimeConfig } from '../utils/runtimeConfig';
import { useAuthContext } from '../context/AuthContext';
import { closeMobileNav } from './BrandHeader';

// Operator pages. `audit-log` is listed only for authenticated users.
const NAV_ITEMS: Array<{ view: ViewType; label: string; requiresAuth?: boolean }> = [
  { view: 'dashboard', label: 'Dashboard' },
  { view: 'action-plan', label: 'Action Plan' },
  { view: 'mempool', label: 'Mempool' },
  { view: 'builder-keys', label: 'Builder Keys' },
  { view: 'registrations', label: 'Registrations' },
  { view: 'bids-won', label: 'Bids Won' },
  { view: 'audit-log', label: 'Audit Log', requiresAuth: true },
  { view: 'api-docs', label: 'API' },
];

function go(e: React.MouseEvent, view: ViewType) {
  e.preventDefault();
  closeMobileNav();
  setView(view);
}

// HeaderNav switches between the public Overview and the Operator pages; the
// operator page links are listed while an operator page is shown.
export const HeaderNav: React.FC = () => {
  const currentView = useView();
  const { overviewURL } = getRuntimeConfig();
  const { isLoggedIn } = useAuthContext();
  const onOverview = currentView === 'overview';

  const navItems = NAV_ITEMS.filter((item) => !item.requiresAuth || isLoggedIn);

  return (
    <>
      <li className="nav-item px-2 d-flex align-items-center">
        <div className="hdr-views" role="group" aria-label="View">
          <a
            href={getViewPath('overview')}
            className={`hdr-view${onOverview ? ' active' : ''}`}
            aria-current={onOverview ? 'page' : undefined}
            onClick={(e) => go(e, 'overview')}
          >
            Overview
          </a>
          <a
            href={getViewPath('dashboard')}
            className={`hdr-view${!onOverview ? ' active' : ''}`}
            aria-current={!onOverview ? 'page' : undefined}
            onClick={(e) => go(e, 'dashboard')}
          >
            Operator
          </a>
        </div>
      </li>
      {!onOverview &&
        navItems.map((item) => (
          <li key={item.view} className="nav-item px-1">
            <a
              href={getViewPath(item.view)}
              className={`nav-link header-nav-button ${currentView === item.view ? 'active' : ''}`}
              aria-current={currentView === item.view ? 'page' : undefined}
              onClick={(e) => go(e, item.view)}
            >
              <span className="nav-text">{item.label}</span>
            </a>
          </li>
        ))}
      {overviewURL && (
        <li className="nav-item px-1">
          <a href={overviewURL} className="nav-link header-nav-button">
            <span className="nav-text">
              All builders <i className="fas fa-arrow-up-right-from-square ms-1 small"></i>
            </span>
          </a>
        </li>
      )}
    </>
  );
};
