import { expect, fetchDaemonFingerprint, openStatusPage, test } from './harness';

const NOT_REACHABLE_TEXT = 'devm daemon not reachable';
const SKEW_TEXT = 'does not match app fingerprint';

test('renders the daemon status screen without an unreachable banner', async ({ page, staticBaseURL, daemonProxy }) => {
  await openStatusPage(page, staticBaseURL, { proxy: daemonProxy });

  // Tolerates either state the real e2e daemon might be in: some
  // projects registered, or none yet.
  await expect(async () => {
    const rowCount = await page.locator('table tbody tr').count();
    const emptyStateCount = await page.getByText('No projects yet.', { exact: false }).count();
    expect(rowCount > 0 || emptyStateCount > 0).toBe(true);
  }).toPass({ timeout: 5000 });

  await expect(page.getByText(NOT_REACHABLE_TEXT)).toHaveCount(0);
});

test('shows the not-reachable state when the daemon is unreachable', async ({ page, staticBaseURL, brokenProxy }) => {
  await openStatusPage(page, staticBaseURL, { proxy: brokenProxy });

  await expect(page.getByText(NOT_REACHABLE_TEXT)).toBeVisible();
});

test('shows the version-skew banner on a fingerprint mismatch', async ({ page, staticBaseURL, daemonProxy }) => {
  await openStatusPage(page, staticBaseURL, { proxy: daemonProxy, fingerprint: 'mismatched-fingerprint' });

  await expect(page.getByText(SKEW_TEXT)).toBeVisible();
});

test('shows no version-skew banner when fingerprints match', async ({ page, staticBaseURL, daemonProxy }) => {
  const fingerprint = await fetchDaemonFingerprint(daemonProxy);

  // Registered before navigation so it catches the /version request
  // ProjectStore.start() fires from onMount — waiting for a positive
  // signal that checkVersion() resolved, rather than a fixed timeout,
  // is what makes "no banner" a real assertion instead of a race.
  const versionChecked = page.waitForResponse((resp) => resp.url() === `${daemonProxy.baseURL}/version`);
  await openStatusPage(page, staticBaseURL, { proxy: daemonProxy, fingerprint });
  await versionChecked;

  await expect(page.getByText(SKEW_TEXT)).toHaveCount(0);
});

test('polls status roughly every second and stays consistent across polls', async ({ page, staticBaseURL, daemonProxy }) => {
  const isStatusPoll = (url: string) => url === `${daemonProxy.baseURL}/status/all`;
  const firstPoll = page.waitForResponse((resp) => isStatusPoll(resp.url()));
  await openStatusPage(page, staticBaseURL, { proxy: daemonProxy });
  await firstPoll;

  const secondPoll = page.waitForResponse((resp) => isStatusPoll(resp.url()));
  await secondPoll;

  // Two polls happened (the 1s interval fired) and the screen settled
  // on a reachable state after both — no flicker into "not reachable"
  // between them.
  await expect(page.getByText(NOT_REACHABLE_TEXT)).toHaveCount(0);
});
