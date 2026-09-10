import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import VersionSkewBanner from './VersionSkewBanner.svelte';
import { ProjectStore } from '../lib/store.svelte';

describe('VersionSkewBanner', () => {
  it('hidden when fingerprints match', () => {
    const store = new ProjectStore();
    store.appFingerprint = 'abc';
    store.daemonFingerprint = 'abc';
    const { container } = render(VersionSkewBanner, { store });
    expect(container.textContent).toBe('');
  });

  it('hidden when either fingerprint is null (still loading)', () => {
    const store = new ProjectStore();
    store.appFingerprint = null;
    store.daemonFingerprint = 'abc';
    const { container } = render(VersionSkewBanner, { store });
    expect(container.textContent).toBe('');
  });

  it('visible when fingerprints differ', () => {
    const store = new ProjectStore();
    store.appFingerprint = 'app-fp';
    store.daemonFingerprint = 'daemon-fp';
    const { getByText } = render(VersionSkewBanner, { store });
    expect(getByText(/does not match/)).toBeTruthy();
  });
});
