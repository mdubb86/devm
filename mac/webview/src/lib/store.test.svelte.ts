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
});
