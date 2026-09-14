import { getStatusAll, getVersion } from './api';
import type { ProjectStatus } from './types';

export class ProjectStore {
  projects = $state<ProjectStatus[]>([]);
  daemonReachable = $state(true);
  lastError = $state<string | null>(null);
  appFingerprint = $state<string | null>(window.__DEVM_APP_FINGERPRINT__ ?? null);
  daemonFingerprint = $state<string | null>(null);
  private timer: ReturnType<typeof setInterval> | null = null;
  private generation = 0;

  get versionSkew(): boolean {
    return (
      this.appFingerprint !== null &&
      this.daemonFingerprint !== null &&
      this.appFingerprint !== this.daemonFingerprint
    );
  }

  start(intervalMs = 1000): void {
    if (this.timer !== null) return;
    this.refreshNow();
    void this.checkVersion();
    this.timer = setInterval(() => {
      this.refreshNow();
    }, intervalMs);
  }

  stop(): void {
    this.generation++;
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  async refreshNow(): Promise<void> {
    const generation = this.generation;
    const wasUnreachable = !this.daemonReachable;
    try {
      const projects = await getStatusAll();
      if (generation !== this.generation) return;
      this.projects = projects;
      this.daemonReachable = true;
      this.lastError = null;
      if (wasUnreachable) {
        void this.checkVersion();
      }
    } catch (e) {
      if (generation !== this.generation) return;
      this.daemonReachable = false;
      this.lastError = e instanceof Error ? e.message : String(e);
      console.error('refreshNow failed:', e instanceof Error ? `${e.name}: ${e.message}` : String(e));
    }
  }

  async checkVersion(): Promise<void> {
    try {
      const build = await getVersion();
      this.daemonFingerprint = build.fingerprint ?? null;
    } catch (e) {
      console.error('checkVersion failed:', e instanceof Error ? `${e.name}: ${e.message}` : String(e));
    }
  }
}
