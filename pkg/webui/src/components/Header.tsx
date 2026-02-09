import React from 'react';
import { HeaderNav } from './HeaderNav';
import { UserDisplay } from './UserDisplay';
import { useTheme, type ThemeMode } from '../hooks/useTheme';
import { setView } from '../stores/viewStore';
import logo from '../assets/buildoor.png';

const THEME_ICONS: Record<ThemeMode, string> = {
  light: '#sun-fill',
  dark: '#moon-stars-fill',
  auto: '#circle-half',
};

export const Header: React.FC = () => {
  const { theme, setTheme } = useTheme();

  return (
    <header className="text-bg-dark header-bar" data-bs-theme="dark">
      <svg xmlns="http://www.w3.org/2000/svg" style={{ display: 'none' }}>
        <symbol id="logo" viewBox="0 0 512 512">
          <path fillRule="evenodd" clipRule="evenodd" d="M256 6.3l8.5 3.2 232 88 15.5 5.9V120 384v16l-14.8 6.1-232 96L256 506l-9.2-3.8-232-96L0 400V384 120 103.4l15.5-5.9 232-88L256 6.3zM48 368l184 76.1V240.4L48 168.1V368zm232 76.1L464 368V168.1L280 240.4V444.1zM256 57.7l-182 69 182 71.5 182-71.5-182-69z" />
        </symbol>
        <symbol id="check2" viewBox="0 0 16 16">
          <path d="M13.854 3.646a.5.5 0 0 1 0 .708l-7 7a.5.5 0 0 1-.708 0l-3.5-3.5a.5.5 0 1 1 .708-.708L6.5 10.293l6.646-6.647a.5.5 0 0 1 .708 0z" />
        </symbol>
        <symbol id="circle-half" viewBox="0 0 16 16">
          <path d="M8 15A7 7 0 1 0 8 1v14zm0 1A8 8 0 1 1 8 0a8 8 0 0 1 0 16z" />
        </symbol>
        <symbol id="sun-fill" viewBox="0 0 16 16">
          <path d="M8 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM8 0a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 0zm0 13a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 13zm8-5a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2a.5.5 0 0 1 .5.5zM3 8a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2A.5.5 0 0 1 3 8zm10.657-5.657a.5.5 0 0 1 0 .707l-1.414 1.415a.5.5 0 1 1-.707-.708l1.414-1.414a.5.5 0 0 1 .707 0zm-9.193 9.193a.5.5 0 0 1 0 .707L3.05 13.657a.5.5 0 0 1-.707-.707l1.414-1.414a.5.5 0 0 1 .707 0zm9.193 2.121a.5.5 0 0 1-.707 0l-1.414-1.414a.5.5 0 0 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .707zM4.464 4.465a.5.5 0 0 1-.707 0L2.343 3.05a.5.5 0 1 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .708z" />
        </symbol>
        <symbol id="moon-stars-fill" viewBox="0 0 16 16">
          <path d="M6 .278a.768.768 0 0 1 .08.858 7.208 7.208 0 0 0-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.787.787 0 0 1 .81.316.733.733 0 0 1-.031.893A8.349 8.349 0 0 1 8.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.752.752 0 0 1 6 .278z" />
          <path d="M10.794 3.148a.217.217 0 0 1 .412 0l.387 1.162c.173.518.579.924 1.097 1.097l1.162.387a.217.217 0 0 1 0 .412l-1.162.387a1.734 1.734 0 0 0-1.097 1.097l-.387 1.162a.217.217 0 0 1-.412 0l-.387-1.162A1.734 1.734 0 0 0 9.31 6.593l-1.162-.387a.217.217 0 0 1 0-.412l1.162-.387a1.734 1.734 0 0 0 1.097-1.097l.387-1.162zM13.863.099a.145.145 0 0 1 .274 0l.258.774c.115.346.386.617.732.732l.774.258a.145.145 0 0 1 0 .274l-.774.258a1.156 1.156 0 0 0-.732.732l-.258.774a.145.145 0 0 1-.274 0l-.258-.774a1.156 1.156 0 0 0-.732-.732l-.774-.258a.145.145 0 0 1 0-.274l.774-.258c.346-.115.617-.386.732-.732L13.863.1z" />
        </symbol>
      </svg>

      <nav id="nav" className="main-navigation navbar navbar-expand-lg navbar-light">
        <div className="container-fluid d-flex">
          <a className="navbar-brand col-10 col-lg-auto me-lg-3" href="/" onClick={(e) => { e.preventDefault(); setView('dashboard'); }}>
            <div className="page-brand">
              <div className="page-brand-container d-flex align-items-center gap-2">
                <img
                  src={logo}
                  alt="Buildoor"
                  className="brand-logo"
                />
                <span className="brand-text page-brand-title">Buildoor</span>
              </div>
            </div>
          </a>

          <div className="toggler-wrapper d-block d-lg-none">
            <button
              className="navbar-toggler"
              type="button"
              data-bs-toggle="collapse"
              data-bs-target="#navbarSupportedContent"
              aria-controls="navbarSupportedContent"
              aria-expanded="false"
              aria-label="Toggle navigation"
            >
              <span className="navbar-toggler-icon"></span>
            </button>
          </div>

          <div className="flex-grow-1 d-none d-lg-block"></div>

          <div className="collapse navbar-collapse" id="navbarSupportedContent">
            <ul className="navbar-nav me-auto">
              <HeaderNav />
            </ul>
            <ul className="navbar-nav ms-auto">
              <li className="nav-item dropdown theme-selector">
                <button
                  className="nav-link dropdown-toggle"
                  id="bd-theme-text"
                  data-bs-toggle="dropdown"
                  aria-haspopup="true"
                  aria-expanded="false"
                >
                  <div className="theme-navicon">
                    <svg className="colormode-icon my-1 theme-icon-active">
                      <use href={THEME_ICONS[theme]}></use>
                    </svg>
                  </div>
                  <span className="nav-text collapsed-info theme-navlabel" id="bd-theme">
                    Theme
                  </span>
                </button>
                <ul className="dropdown-menu dropdown-menu-end" aria-labelledby="bd-theme-text">
                  {(['light', 'dark', 'auto'] as ThemeMode[]).map((mode) => (
                    <li key={mode}>
                      <button
                        type="button"
                        className={`dropdown-item d-flex align-items-center ${theme === mode ? 'active' : ''}`}
                        aria-pressed={theme === mode}
                        onClick={() => setTheme(mode)}
                      >
                        <svg className="colormode-icon me-2 opacity-50 theme-icon" style={{ width: '20px' }}>
                          <use href={THEME_ICONS[mode]}></use>
                        </svg>
                        {mode === 'auto' ? 'Auto' : mode.charAt(0).toUpperCase() + mode.slice(1)}
                        <svg className={`colormode-icon ms-auto ${theme === mode ? '' : 'd-none'}`}>
                          <use href="#check2"></use>
                        </svg>
                      </button>
                    </li>
                  ))}
                </ul>
              </li>

              <li className="nav-item ms-3 d-flex align-items-center">
                <UserDisplay />
              </li>
            </ul>
          </div>
        </div>
      </nav>
    </header>
  );
};
