import { useEffect, useState } from 'react';

/**
 * Hash routing (DESIGN.md §9.5) — no router dependency, so the server's SPA
 * fallback needs no route table: `#/` (or no hash) is the overview — which
 * standalone instances render as the single-server dashboard — and
 * `#/s/{id}` is one server's full dashboard.
 */
export type Route = { page: 'overview' } | { page: 'server'; id: string };

export function parseRoute(hash: string): Route {
  const m = /^#\/s\/([^/?#]+)/.exec(hash);
  // The segment is used verbatim — server ids are `[a-z0-9-]{1,32}` or
  // "local", so nothing legitimate needs decoding, and decodeURIComponent
  // throws on malformed input (`#/s/100%`), which would crash the app inside
  // the useState initializer. Unknown ids fall back to the overview in App.
  if (m) return { page: 'server', id: m[1] };
  return { page: 'overview' };
}

/** The current hash route, re-parsed on every hashchange. */
export function useHashRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));

  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);

  return route;
}
