import React from 'react';
import { setView, useView, type ViewType } from '../stores/viewStore';

const NAV_ITEMS: Array<{ view: ViewType; label: string }> = [
  { view: 'dashboard', label: 'Dashboard' },
  { view: 'bids-won', label: 'Bids Won' },
  { view: 'validators', label: 'Validators' },
  { view: 'api-docs', label: 'API' },
];

export const HeaderNav: React.FC = () => {
  const currentView = useView();

  return (
    <>
      {NAV_ITEMS.map((item) => (
        <li key={item.view} className="nav-item px-2">
          <button
            type="button"
            className={`nav-link header-nav-button ${currentView === item.view ? 'active' : ''}`}
            onClick={() => setView(item.view)}
          >
            <span className="nav-text">{item.label}</span>
          </button>
        </li>
      ))}
    </>
  );
};
