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

async function fetchJSON<T>(url: string): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(url);
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
  return fetchJSON<ProjectStatus[]>('devm-api://vm/status/all');
}

export function getVersion(): Promise<Build> {
  return fetchJSON<Build>('devm-api://vm/version');
}

export function getHandshake(name?: string): Promise<HandshakeResponse> {
  const query = name ? `?name=${encodeURIComponent(name)}` : '';
  return fetchJSON<HandshakeResponse>(`devm-api://vm/handshake${query}`);
}
