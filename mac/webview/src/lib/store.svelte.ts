import { getStatusAll } from './api';
import type { ProjectStatus } from './types';

export class ProjectStore {
  projects = $state<ProjectStatus[]>([]);
  daemonReachable = $state(true);
  lastError = $state<string | null>(null);
  private timer: ReturnType<typeof setInterval> | null = null;
  private generation = 0;

  start(intervalMs = 1000): void {
    if (this.timer !== null) return;
    this.refreshNow();
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
    try {
      const projects = await getStatusAll();
      if (generation !== this.generation) return;
      this.projects = projects;
      this.daemonReachable = true;
      this.lastError = null;
    } catch (e) {
      if (generation !== this.generation) return;
      this.daemonReachable = false;
      this.lastError = e instanceof Error ? e.message : String(e);
    }
  }
}
