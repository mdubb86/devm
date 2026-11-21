import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import StatusScreen from './StatusScreen.svelte';
import { ProjectStore } from '../lib/store.svelte';

describe('StatusScreen', () => {
  it('renders empty state when no projects', () => {
    const store = new ProjectStore();
    store.daemonReachable = true;
    const { getByText } = render(StatusScreen, { store });
    expect(getByText(/No projects yet/)).toBeTruthy();
  });

  it('renders daemon-down state when unreachable', () => {
    const store = new ProjectStore();
    store.daemonReachable = false;
    const { getByText } = render(StatusScreen, { store });
    expect(getByText(/devm daemon not reachable/)).toBeTruthy();
  });

  it('renders one row per project', () => {
    const store = new ProjectStore();
    store.daemonReachable = true;
    store.projects = [
      { name: 'a', vm_running: true, proxy: { status: 'ok', needs_secrets: false } },
      { name: 'b', vm_running: false, proxy: { status: 'missing', needs_secrets: false } },
    ];
    const { getByText } = render(StatusScreen, { store });
    expect(getByText('a')).toBeTruthy();
    expect(getByText('b')).toBeTruthy();
  });

  it('renders an orphaned row with the diverged approve state', () => {
    const store = new ProjectStore();
    store.daemonReachable = true;
    store.projects = [
      {
        name: 'c',
        vm_running: true,
        orphaned: true,
        mac_cwd: '/Users/mike/code/devm',
        proxy: { status: 'stale', needs_secrets: false },
        approve_state: { diverged: true },
      },
    ];
    const { getByText } = render(StatusScreen, { store });
    expect(getByText('orphaned')).toBeTruthy();
    expect(getByText('stale')).toBeTruthy();
    expect(getByText('diverged')).toBeTruthy();
    expect(getByText('~/code/devm')).toBeTruthy();
  });
});
