import { useCallback, useEffect, useState } from 'react';

export type Theme = 'dark' | 'light';

/**
 * Theme state, kept on <html data-theme>. The initial value was resolved by
 * the inline script in index.html; localStorage is written only on an
 * explicit toggle so untouched installs keep following the OS preference.
 */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() =>
    document.documentElement.dataset.theme === 'light' ? 'light' : 'dark',
  );

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  const toggle = useCallback(() => {
    setTheme((t) => {
      const next: Theme = t === 'dark' ? 'light' : 'dark';
      try {
        localStorage.setItem('owlwatch-theme', next);
      } catch {
        /* storage unavailable — the toggle still works for this session */
      }
      return next;
    });
  }, []);

  return [theme, toggle];
}
