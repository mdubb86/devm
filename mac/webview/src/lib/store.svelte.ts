import { getStatusAll } from './api';
import type { ProjectStatus } from './types';

export class ProjectStore {
  projects = $state<ProjectStatus[]>([]);
  daemonReachable = $state(true);
  lastError = $state<string | null>(null);
  private timer: ReturnType<typeof setInterval> | null = null;

  start(intervalMs = 1000): void {
    if (this.timer !== null) return;
    this.refreshNow();
    this.timer = setInterval(() => {
      this.refreshNow();
    }, intervalMs);
  }

  stop(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  async refreshNow(): Promise<void> {
    try {
      this.projects = await getStatusAll();
      this.daemonReachable = true;
      this.lastError = null;
    } catch (e) {
      this.daemonReachable = false;
      this.lastError = e instanceof Error ? e.message : String(e);
    }
  }
}
