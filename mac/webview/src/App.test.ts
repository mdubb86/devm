import { render, screen } from '@testing-library/svelte';
import { vi } from 'vitest';
import App from './App.svelte';

describe('App', () => {
  it('mounts StatusScreen and shows the empty state before the daemon responds', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})));
    const { unmount } = render(App);
    expect(screen.getByText(/No projects yet/)).toBeTruthy();
    unmount();
  });
});
