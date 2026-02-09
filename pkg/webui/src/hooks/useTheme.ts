import { useCallback, useEffect, useMemo, useState } from 'react';

export type ThemeMode = 'light' | 'dark' | 'auto';

const storageKey = 'theme';

const getStoredTheme = (): ThemeMode | null => {
  const stored = localStorage.getItem(storageKey);
  if (stored === 'light' || stored === 'dark' || stored === 'auto') {
    return stored;
  }
  return null;
};

const getPreferredTheme = (): ThemeMode => {
  const stored = getStoredTheme();
  return stored ?? 'auto';
};

const getResolvedTheme = (mode: ThemeMode) => {
  if (mode !== 'auto') return mode;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
};

export function useTheme() {
  const [theme, setThemeState] = useState<ThemeMode>(() => getPreferredTheme());

  const setTheme = useCallback((mode: ThemeMode) => {
    localStorage.setItem(storageKey, mode);
    setThemeState(mode);
  }, []);

  const resolvedTheme = useMemo(() => getResolvedTheme(theme), [theme]);

  useEffect(() => {
    document.documentElement.setAttribute('data-bs-theme', resolvedTheme);
  }, [resolvedTheme]);

  useEffect(() => {
    if (theme !== 'auto') return;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const handler = () => {
      document.documentElement.setAttribute('data-bs-theme', getResolvedTheme('auto'));
    };
    media.addEventListener('change', handler);
    return () => media.removeEventListener('change', handler);
  }, [theme]);

  return { theme, resolvedTheme, setTheme };
}
