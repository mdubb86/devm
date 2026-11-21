import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ProjectStore } from './store.svelte';

const okResponse = (data: unknown) => new Response(JSON.stringify(data), { status: 200 });

describe('ProjectStore', () => {
  let mockFetch: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    mockFetch = vi.fn();
    vi.stubGlobal('fetch', mockFetch);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('populates projects after start()', async () => {
    mockFetch.mockResolvedValue(
      okResponse([{ name: 'p', vm_running: true, proxy: { status: 'ok', needs_secrets: false } }]),
    );
    const store = new ProjectStore();
    store.start();
    // Advancing by 0ms flushes the microtask chain of the immediate refresh
    // without crossing the 1000ms interval delay.
    await vi.advanceTimersByTimeAsync(0);
    expect(store.projects).toHaveLength(1);
    expect(store.projects[0].name).toBe('p');
    expect(store.daemonReachable).toBe(true);
    store.stop();
  });

  it('sets daemonReachable false on fetch failure', async () => {
    mockFetch.mockRejectedValue(new Error('nope'));
    const store = new ProjectStore();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(store.daemonReachable).toBe(false);
    expect(store.lastError).toMatch(/daemon unreachable/);
    store.stop();
  });

  it('stop() cancels further polls', async () => {
    mockFetch.mockResolvedValue(okResponse([]));
    const store = new ProjectStore();
    store.start();
    await vi.advanceTimersByTimeAsync(0);
    store.stop();
    mockFetch.mockClear();
    await vi.advanceTimersByTimeAsync(3000);
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it('discards a fetch that resolves after stop() was called', async () => {
    let resolveFetch: (r: Response) => void;
    mockFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve;
      }),
    );
    const store = new ProjectStore();
    store.start();
    // refreshNow() has fired and is awaiting the still-pending fetch.
    store.stop();
    resolveFetch!(okResponse([{ name: 'stale', vm_running: true, proxy: { status: 'ok', needs_secrets: false } }]));
    await vi.advanceTimersByTimeAsync(0);
    expect(store.projects).toHaveLength(0);
    expect(store.daemonReachable).toBe(true);
  });

  it('checkVersion() sets daemonFingerprint from getVersion() response', async () => {
    mockFetch.mockResolvedValue(
      okResponse({ version: '1.0.0', commit: 'abc', date: '2026-01-01', fingerprint: 'deadbeef' }),
    );
    const store = new ProjectStore();
    await store.checkVersion();
    expect(store.daemonFingerprint).toBe('deadbeef');
  });

  it('versionSkew is true when fingerprints differ', () => {
    const store = new ProjectStore();
    store.appFingerprint = 'app-fp';
    store.daemonFingerprint = 'daemon-fp';
    expect(store.versionSkew).toBe(true);
  });

  it('versionSkew is false when fingerprints match', () => {
    const store = new ProjectStore();
    store.appFingerprint = 'fp';
    store.daemonFingerprint = 'fp';
    expect(store.versionSkew).toBe(false);
  });

  it('versionSkew is false when either fingerprint is null', () => {
    const store = new ProjectStore();
    store.appFingerprint = null;
    store.daemonFingerprint = 'daemon-fp';
    expect(store.versionSkew).toBe(false);

    store.appFingerprint = 'app-fp';
    store.daemonFingerprint = null;
    expect(store.versionSkew).toBe(false);
  });
});
