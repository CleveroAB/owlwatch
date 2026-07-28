import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  connectOverview,
  fetchAlertsInfo,
  fetchServers,
  onUnauthorized,
  sendTestAlertEmail,
  setToken,
  UnauthorizedError,
} from './api';

/** Minimal localStorage so get/setToken work outside a browser. */
function stubStorage(): void {
  const store = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
  });
}

function respond(status: number, body = '{}'): Response {
  return new Response(body, { status, headers: { 'Content-Type': 'application/json' } });
}

describe('onUnauthorized notifier', () => {
  beforeEach(() => stubStorage());
  afterEach(() => {
    onUnauthorized(null);
    vi.unstubAllGlobals();
  });

  it('notifies the listener on a 401 and still throws UnauthorizedError', async () => {
    setToken('stale');
    vi.stubGlobal('fetch', vi.fn(async () => respond(401, '{"error":"unauthorized"}')));
    const listener = vi.fn();
    onUnauthorized(listener);

    await expect(fetchServers()).rejects.toBeInstanceOf(UnauthorizedError);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('does not notify on non-401 failures', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => respond(500, '{}')));
    const listener = vi.fn();
    onUnauthorized(listener);

    await expect(fetchServers()).rejects.toThrow('HTTP 500');
    expect(listener).not.toHaveBeenCalled();
  });

  it('ignores a 401 for a request sent with a since-rotated token', async () => {
    // The user saved a new token while this request (sent with the old one)
    // was in flight — its 401 must not re-open the gate they just cleared.
    setToken('old');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        setToken('new');
        return respond(401, '{"error":"unauthorized"}');
      }),
    );
    const listener = vi.fn();
    onUnauthorized(listener);

    await expect(fetchServers()).rejects.toBeInstanceOf(UnauthorizedError);
    expect(listener).not.toHaveBeenCalled();
  });

  it('stops notifying once the listener is cleared', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => respond(401, '{"error":"unauthorized"}')));
    const listener = vi.fn();
    onUnauthorized(listener);
    onUnauthorized(null);

    await expect(fetchServers()).rejects.toBeInstanceOf(UnauthorizedError);
    expect(listener).not.toHaveBeenCalled();
  });

  it('sends the bearer token and succeeds silently on 200', async () => {
    setToken('t0k3n');
    const fetchMock = vi.fn<typeof fetch>(async () => respond(200, '[]'));
    vi.stubGlobal('fetch', fetchMock);
    const listener = vi.fn();
    onUnauthorized(listener);

    await expect(fetchServers()).resolves.toEqual([]);
    expect(listener).not.toHaveBeenCalled();
    const [, init] = fetchMock.mock.calls[0]!;
    expect(init?.headers).toEqual({ Authorization: 'Bearer t0k3n' });
  });
});

describe('authenticated fetch streaming', () => {
  beforeEach(() => {
    stubStorage();
    vi.stubGlobal('window', { setTimeout, clearTimeout });
  });
  afterEach(() => {
    onUnauthorized(null);
    vi.unstubAllGlobals();
  });

  it('sends the bearer header and parses SSE without a query token', async () => {
    setToken('stream-secret');
    const bytes = new TextEncoder().encode(
      'event: servers\ndata: [{"id":"local","name":"local","local":true,"online":true,"lastSeen":0,"intervalMs":2000}]\n\n',
    );
    const fetchMock = vi.fn<typeof fetch>(async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(bytes);
            controller.close();
          },
        }),
        { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
      ),
    );
    vi.stubGlobal('fetch', fetchMock);
    const onServers = vi.fn();
    const stop = connectOverview({
      onServers,
      onSnapshot: vi.fn(),
      onStatus: vi.fn(),
      onState: vi.fn(),
    });
    await vi.waitFor(() => expect(onServers).toHaveBeenCalledTimes(1));
    stop();

    const [path, init] = fetchMock.mock.calls[0]!;
    expect(path).toBe('/api/overview/live');
    expect(path).not.toContain('token=');
    expect(init?.headers).toMatchObject({ Authorization: 'Bearer stream-secret' });
  });

  it('surfaces a stream 401 immediately after token rotation', async () => {
    setToken('expired-stream-secret');
    vi.stubGlobal('fetch', vi.fn(async () => respond(401, '{"error":"unauthorized"}')));
    const listener = vi.fn();
    onUnauthorized(listener);
    const stop = connectOverview({
      onServers: vi.fn(),
      onSnapshot: vi.fn(),
      onStatus: vi.fn(),
      onState: vi.fn(),
    });
    await vi.waitFor(() => expect(listener).toHaveBeenCalledTimes(1));
    stop();
  });
});

describe('email alerts API', () => {
  beforeEach(() => stubStorage());
  afterEach(() => {
    onUnauthorized(null);
    vi.unstubAllGlobals();
  });

  it('sends the test request as an authenticated POST', async () => {
    setToken('secret-token-1234');
    const fetchMock = vi.fn(async () => respond(200, '{"ok":true}'));
    vi.stubGlobal('fetch', fetchMock);

    await sendTestAlertEmail();
    const [path, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).toBe('/api/alerts/test');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>).Authorization).toBe(
      'Bearer secret-token-1234',
    );
  });

  it('surfaces the server error message on a failed send', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => respond(502, '{"error":"sending the test email failed: auth failed"}')),
    );
    await expect(sendTestAlertEmail()).rejects.toThrow('auth failed');
  });

  it('reports 401 through the unauthorized notifier', async () => {
    setToken('stale');
    vi.stubGlobal('fetch', vi.fn(async () => respond(401, '{"error":"unauthorized"}')));
    const listener = vi.fn();
    onUnauthorized(listener);

    await expect(sendTestAlertEmail()).rejects.toBeInstanceOf(UnauthorizedError);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('parses the enabled flag', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => respond(200, '{"enabled":true}')));
    await expect(fetchAlertsInfo()).resolves.toEqual({ enabled: true });
  });
});
