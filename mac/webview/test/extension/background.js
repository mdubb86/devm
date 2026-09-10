// Test-only Chromium MV3 extension: rewrites the webview's devm-api://
// fetches to the loopback devm-testproxy (bin/devm-testproxy) so a real
// Chromium page can drive the same api.ts calls the native WKWebView
// makes, against a real daemon.
//
// The proxy port isn't known at build time, so the redirect rule is
// installed dynamically (chrome.declarativeNetRequest.updateDynamicRules)
// once the Playwright harness writes it to chrome.storage.local under the
// key "proxyPort" (e.g. chrome.storage.local.set({ proxyPort: 18099 })
// before navigating the page under test).
//
// The rewrite recombines the devm-api:// URL's host and path into a
// single daemon-facing path, mirroring
// mac/devm/Sources/devm/DevmAPIURLSchemeHandler.swift's daemonPath(for:):
// "devm-api://vm/status/all?project=p" -> "http://127.0.0.1:<port>/vm/status/all?project=p".
// Both sides must agree, or the browser-driven tests exercise different
// routing than the native app does.

const RULE_ID = 1;

async function installRedirectRule() {
  const { proxyPort } = await chrome.storage.local.get('proxyPort');
  if (!proxyPort) return;

  const rule = {
    id: RULE_ID,
    priority: 1,
    action: {
      type: 'redirect',
      redirect: {
        // Group 1 is the devm-api:// host (e.g. "vm"); group 2 is
        // everything after it verbatim (path and/or query, already
        // starting with "/" or "?", or empty). \1\2 reproduces the
        // "/" + host + path (+ query) daemon path exactly.
        regexSubstitution: `http://127.0.0.1:${proxyPort}/\\1\\2`,
      },
    },
    condition: {
      regexFilter: '^devm-api://([^/?]+)(.*)$',
      resourceTypes: ['main_frame', 'sub_frame', 'xmlhttprequest', 'other'],
    },
  };

  await chrome.declarativeNetRequest.updateDynamicRules({
    removeRuleIds: [RULE_ID],
    addRules: [rule],
  });
}

chrome.runtime.onInstalled.addListener(installRedirectRule);
chrome.runtime.onStartup.addListener(installRedirectRule);
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && 'proxyPort' in changes) installRedirectRule();
});
