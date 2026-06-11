import React from 'react';
import { getViewPath, setView, useView, type ViewType } from '../stores/viewStore';
import { getRuntimeConfig } from '../utils/runtimeConfig';
import { closeMobileNav } from './BrandHeader';

const NAV_ITEMS: Array<{ view: ViewType; label: string }> = [
  { view: 'dashboard', label: 'Dashboard' },
  { view: 'bids-won', label: 'Bids Won' },
  { view: 'validators', label: 'Validators' },
  { view: 'proposer-preferences', label: 'Proposer Prefs' },
  { view: 'api-docs', label: 'API' },
];

export const HeaderNav: React.FC = () => {
  const currentView = useView();
  const { overviewURL } = getRuntimeConfig();

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
      {NAV_ITEMS.map((item) => (
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
