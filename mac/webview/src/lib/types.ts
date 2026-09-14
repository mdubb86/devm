export interface Build {
  version: string;
  commit: string;
  date: string;
  fingerprint?: string;
  binary_path?: string;
}

export type ProxyStatus = 'ok' | 'missing' | 'stale';

export interface ProxyHealth {
  status: ProxyStatus;
  needs_secrets: boolean;
  rebind?: unknown;
}

export interface ApproveStateSummary {
  diverged: boolean;
}

export interface ProjectStatus {
  name: string;
  vm_running: boolean;
  proxy: ProxyHealth;
  orphaned?: boolean;
  mac_cwd?: string;
  approve_state?: ApproveStateSummary;
}

export interface HandshakeResponse {
  build: Build;
  proxy?: ProxyHealth;
}
