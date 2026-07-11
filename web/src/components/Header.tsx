import { useEffect, useReducer } from 'react';
import type { ConnectionState } from '../lib/api';
import { formatUptime } from '../lib/format';
import type { HostInfo, ServerSummary } from '../lib/types';
import type { Theme } from '../hooks/useTheme';

/** Hub-mode extras for a server page: back link + server switcher (§9.5). */
export interface HubNav {
  servers: ServerSummary[];
  currentId: string;
}

export function Header({
  host,
  status,
  theme,
  onToggleTheme,
  hubNav,
}: {
  host: HostInfo | null;
  status: ConnectionState;
  theme: Theme;
  onToggleTheme: () => void;
  /** Present only on a hub's server pages — standalone renders exactly as v1. */
  hubNav?: HubNav;
}) {
  return (
    <header className="site-header">
      {hubNav && (
        <a className="back-link" href="#/">
          ← Overview
        </a>
      )}
      <div className="brand">
        <span className="brand-mark" aria-hidden="true">
          🦉
        </span>
        <span className="brand-name">owlwatch</span>
      </div>
      {host && (
        <>
          <span className="header-sep" aria-hidden="true">
            ·
          </span>
          <span className="hostname">{host.hostname}</span>
          <span className="chip" title={host.kernelVersion ? `kernel ${host.kernelVersion}` : undefined}>
            {host.platform} · {host.arch}
          </span>
          <span className="uptime">
            up <Uptime bootTime={host.bootTime} />
          </span>
        </>
      )}
      {hubNav && (
        <select
          className="server-select"
          aria-label="Switch server"
          value={hubNav.currentId}
          onChange={(e) => {
            window.location.hash = `#/s/${encodeURIComponent(e.target.value)}`;
          }}
        >
          {hubNav.servers.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      )}
      <div className="spacer" />
      <ConnStatus status={status} />
      <button
        type="button"
        className="icon-btn"
        onClick={onToggleTheme}
        aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
        title={theme === 'dark' ? 'Light theme' : 'Dark theme'}
      >
        {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
      </button>
    </header>
  );
}

/** Live-ticking uptime, isolated so the 1s interval re-renders only this text. */
function Uptime({ bootTime }: { bootTime: number }) {
  const [, tick] = useReducer((c: number) => c + 1, 0);
  useEffect(() => {
    const timer = window.setInterval(tick, 1000);
    return () => window.clearInterval(timer);
  }, []);
  return <>{formatUptime(Date.now() / 1000 - bootTime)}</>;
}

const CONN_META: Record<ConnectionState, { color: string; label: string }> = {
  open: { color: 'var(--status-good)', label: 'Live' },
  reconnecting: { color: 'var(--status-warn)', label: 'Reconnecting…' },
  connecting: { color: 'var(--ink-muted)', label: 'Connecting…' },
};

function ConnStatus({ status }: { status: ConnectionState }) {
  const meta = CONN_META[status];
  return (
    <span className="conn" role="status">
      <span className="conn-dot" style={{ background: meta.color }} aria-hidden="true" />
      {meta.label}
    </span>
  );
}

function SunIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      aria-hidden="true"
    >
      <circle cx="8" cy="8" r="3.1" />
      <path d="M8 1.3v1.7M8 13v1.7M1.3 8H3M13 8h1.7M3.3 3.3l1.2 1.2M11.5 11.5l1.2 1.2M12.7 3.3l-1.2 1.2M4.5 11.5l-1.2 1.2" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M13.3 10.1A5.9 5.9 0 0 1 5.9 2.7a5.9 5.9 0 1 0 7.4 7.4z" />
    </svg>
  );
}
