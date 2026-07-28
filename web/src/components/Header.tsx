import { useEffect, useReducer, useRef, useState } from 'react';
import {
  clearToken,
  fetchAlertsInfo,
  getToken,
  sendTestAlertEmail,
  type ConnectionState,
} from '../lib/api';
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
      <TestEmailButton />
      {getToken() && (
        <button
          type="button"
          className="icon-btn"
          onClick={() => {
            clearToken();
            window.location.reload();
          }}
          aria-label="Forget access token"
          title="Forget access token"
        >
          <LogoutIcon />
        </button>
      )}
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

type TestEmailState =
  | { phase: 'idle' }
  | { phase: 'sending' }
  | { phase: 'sent' }
  | { phase: 'error'; message: string };

/** How long the sent/error feedback chip stays up before reverting. */
const TEST_EMAIL_FEEDBACK_MS = 6000;

/**
 * "Send test email" button (DESIGN.md §3.4). Rendered only when the instance
 * serving the UI has email alerting configured — /api/alerts says so — which
 * keeps the header pixel-identical to v1 on every other deployment.
 */
function TestEmailButton() {
  const [enabled, setEnabled] = useState(false);
  const [state, setState] = useState<TestEmailState>({ phase: 'idle' });
  const resetTimer = useRef<number | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    fetchAlertsInfo(ctrl.signal)
      .then((info) => setEnabled(info.enabled))
      .catch(() => {
        /* status unknown (offline, 401, old server) — keep the button hidden */
      });
    return () => {
      ctrl.abort();
      if (resetTimer.current !== null) window.clearTimeout(resetTimer.current);
    };
  }, []);

  if (!enabled) return null;

  const send = () => {
    if (resetTimer.current !== null) window.clearTimeout(resetTimer.current);
    setState({ phase: 'sending' });
    sendTestAlertEmail()
      .then(() => setState({ phase: 'sent' }))
      .catch((err: unknown) =>
        setState({ phase: 'error', message: err instanceof Error ? err.message : String(err) }),
      )
      .finally(() => {
        resetTimer.current = window.setTimeout(
          () => setState({ phase: 'idle' }),
          TEST_EMAIL_FEEDBACK_MS,
        );
      });
  };

  return (
    <>
      {state.phase === 'sent' && (
        <span className="chip" role="status">
          ✓ Test email sent
        </span>
      )}
      {state.phase === 'error' && (
        <span className="chip test-email-error" role="alert" title={state.message}>
          ✕ {state.message}
        </span>
      )}
      <button
        type="button"
        className="icon-btn"
        disabled={state.phase === 'sending'}
        onClick={send}
        aria-label="Send a test alert email"
        title="Send a test alert email"
      >
        {state.phase === 'sending' ? <EnvelopeDotsIcon /> : <EnvelopeIcon />}
      </button>
    </>
  );
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

function EnvelopeIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="1.8" y="3.2" width="12.4" height="9.6" rx="1.3" />
      <path d="M2.2 4.2 8 8.8l5.8-4.6" />
    </svg>
  );
}

/** Envelope with ellipsis, shown while the test send is in flight. */
function EnvelopeDotsIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect
        x="1.8"
        y="3.2"
        width="12.4"
        height="9.6"
        rx="1.3"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <circle cx="5.2" cy="8" r="0.9" fill="currentColor" />
      <circle cx="8" cy="8" r="0.9" fill="currentColor" />
      <circle cx="10.8" cy="8" r="0.9" fill="currentColor" />
    </svg>
  );
}

function LogoutIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      aria-hidden="true"
    >
      <path
        d="M6.5 2.5H3.8a1.3 1.3 0 0 0-1.3 1.3v8.4a1.3 1.3 0 0 0 1.3 1.3h2.7M9.5 5l3 3-3 3M12.5 8H6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
