declare global {
  interface Window {
    __DEVM_APP_FINGERPRINT__?: string;
    __DEVM_APP_VERSION__?: string;
    // Set only by the Playwright harness (test/e2e/harness.ts), pointing
    // at devm-testproxy's TCP listener. Unset in production, where api.ts
    // falls back to the devm-api:// scheme WKWebView routes natively.
    __DEVM_API_BASE__?: string;
  }
}

export {};
