import type { Build, HandshakeResponse, ProjectStatus } from './types';

export class DaemonUnreachableError extends Error {
  constructor(cause: unknown) {
    super(`daemon unreachable: ${cause instanceof Error ? cause.message : String(cause)}`);
    this.name = 'DaemonUnreachableError';
  }
}

export class DaemonHTTPError extends Error {
  constructor(
    public status: number,
    public body: string,
  ) {
    super(`daemon returned ${status}: ${body}`);
    this.name = 'DaemonHTTPError';
  }
}

// In production, WKWebView routes devm-api:// natively (see
// DevmAPIURLSchemeHandler.swift) straight to the daemon's Unix socket, so
// the base stays "devm-api://vm". The Playwright harness (test/e2e/harness.ts)
// can't dial devm-api:// from Chromium at all, so it points the base at
// devm-testproxy's TCP listener instead via window.__DEVM_API_BASE__ — e.g.
// "http://127.0.0.1:PORT", with no "/vm" segment, since devm-testproxy
// forwards paths straight through to the daemon's actual routes
// (/status/all, /version, /handshake — see internal/serviceapi), which
// aren't "/vm"-prefixed.
function resolveBaseURL(): string {
  return window.__DEVM_API_BASE__ ?? 'devm-api://vm';
}

async function fetchJSON<T>(path: string): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(`${resolveBaseURL()}${path}`);
  } catch (e) {
    throw new DaemonUnreachableError(e);
  }
  if (!resp.ok) {
    const body = await resp.text().catch(() => '');
    throw new DaemonHTTPError(resp.status, body);
  }
  return resp.json();
}

export function getStatusAll(): Promise<ProjectStatus[]> {
  return fetchJSON<ProjectStatus[]>('/status/all');
}

export function getVersion(): Promise<Build> {
  return fetchJSON<Build>('/version');
}

export function getHandshake(name?: string): Promise<HandshakeResponse> {
  const query = name ? `?name=${encodeURIComponent(name)}` : '';
  return fetchJSON<HandshakeResponse>(`/handshake${query}`);
}
