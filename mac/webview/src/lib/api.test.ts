import { getStatusAll, getVersion, getHandshake, DaemonUnreachableError, DaemonHTTPError } from './api';

describe('api', () => {
  let mockFetch: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mockFetch = vi.fn();
    vi.stubGlobal('fetch', mockFetch);
  });

  it('getStatusAll parses JSON array', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify([{ name: 'p', vm_running: true, proxy: { status: 'ok', needs_secrets: false } }]),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    const rows = await getStatusAll();
    expect(rows).toHaveLength(1);
    expect(rows[0].name).toBe('p');
    expect(rows[0].proxy.status).toBe('ok');
    expect(mockFetch).toHaveBeenCalledWith('devm-api://vm/status/all');
  });

  it('getVersion parses Build', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ version: '0.1.0', commit: 'abc', date: '2026-09-09', fingerprint: 'fp', binary_path: '/x' }),
        { status: 200 },
      ),
    );
    const b = await getVersion();
    expect(b.version).toBe('0.1.0');
    expect(b.fingerprint).toBe('fp');
    expect(mockFetch).toHaveBeenCalledWith('devm-api://vm/version');
  });

  it('getHandshake omits query param when no name given', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ build: { version: '0.1.0', commit: 'x', date: '2026-09-09' } }),
        { status: 200 },
      ),
    );
    await getHandshake();
    expect(mockFetch).toHaveBeenCalledWith('devm-api://vm/handshake');
  });

  it('getHandshake passes name query', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ build: { version: '0.1.0', commit: 'x', date: '2026-09-09' } }),
        { status: 200 },
      ),
    );
    await getHandshake('proj');
    expect(mockFetch).toHaveBeenCalledWith('devm-api://vm/handshake?name=proj');
  });

  it('getHandshake URL-encodes the name query param', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ build: { version: '0.1.0', commit: 'x', date: '2026-09-09' } }),
        { status: 200 },
      ),
    );
    await getHandshake('proj name');
    expect(mockFetch).toHaveBeenCalledWith('devm-api://vm/handshake?name=proj%20name');
  });

  it('throws DaemonUnreachableError on network failure', async () => {
    mockFetch.mockRejectedValueOnce(new Error('network fail'));
    await expect(getStatusAll()).rejects.toBeInstanceOf(DaemonUnreachableError);
  });

  it('throws DaemonHTTPError on non-2xx', async () => {
    mockFetch.mockResolvedValueOnce(new Response('bad', { status: 500 }));
    const err = await getStatusAll().catch((e) => e);
    expect(err).toBeInstanceOf(DaemonHTTPError);
    expect(err.status).toBe(500);
    expect(err.body).toBe('bad');
  });
});
