declare global {
  interface Window {
    __DEVM_APP_FINGERPRINT__?: string;
    __DEVM_APP_VERSION__?: string;
  }
}

export {};
