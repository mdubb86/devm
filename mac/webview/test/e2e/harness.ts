// Shared Playwright fixtures for the mac/webview status-screen e2e suite.
//
// These tests drive the real gui.html + gui.js bundle in a real Chromium
// page against the real e2e daemon — no mocks. Two things make that
// possible without a Chromium extension (T14's first attempt found
// Chromium rejects fetch() to a custom devm-api:// scheme before any
// extension hook can fire):
//
//  1. api.ts (mac/webview/src/lib/api.ts) reads an injectable
//     window.__DEVM_API_BASE__. Production leaves it unset and falls
//     back to devm-api://vm; openStatusPage() below sets it to
//     devm-testproxy's TCP address instead.
//  2. gui.html is served over plain HTTP (staticServer below) rather
//     than opened as a file:// URL — Chromium enforces CORS for
//     cross-origin fetch() from a file:// (opaque) origin exactly like
//     it does across ports, so file:// wouldn't avoid needing the CORS
//     header devm-testproxy now sends (cmd/devm-testproxy/main.go).
import { test as base, expect, type Page } from '@playwright/test';
import { type ChildProcessWithoutNullStreams, spawn } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import http, { type Server } from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, '../../../..');
const TESTPROXY_BIN = path.join(REPO_ROOT, 'bin/devm-testproxy');
const GUI_RESOURCES_DIR = path.join(REPO_ROOT, 'mac/devm/Resources');
const GUI_HTML = path.join(GUI_RESOURCES_DIR, 'gui.html');
const E2E_SOCKET = path.join(os.homedir(), 'Library/Application Support/devm-e2e/devm.sock');

function assertPreconditions(): void {
  if (!existsSync(TESTPROXY_BIN)) {
    throw new Error(`${TESTPROXY_BIN} not found — run "just build-testproxy" first.`);
  }
  if (!existsSync(GUI_HTML)) {
    throw new Error(`${GUI_HTML} not found — run "just mac-webview-build" first.`);
  }
}

function assertE2eDaemonRunning(): void {
  if (!existsSync(E2E_SOCKET)) {
    throw new Error(
      `e2e daemon socket not found at ${E2E_SOCKET} — start the e2e daemon ` +
        '("just e2e-bootstrap" installs it) before running this suite.',
    );
  }
}

async function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (addr === null || typeof addr === 'string') {
        reject(new Error('failed to allocate a free TCP port'));
        return;
      }
      const port = addr.port;
      srv.close((err) => (err ? reject(err) : resolve(port)));
    });
  });
}

async function waitForListening(port: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      // Any response — even a 404 or 502 — proves the TCP listener is up;
      // readiness here means "accepting connections," not "daemon healthy".
      await fetch(`http://127.0.0.1:${port}/`);
      return;
    } catch (e) {
      lastError = e;
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  throw new Error(`nothing listening on 127.0.0.1:${port} within ${timeoutMs}ms: ${String(lastError)}`);
}

export interface TestProxy {
  port: number;
  baseURL: string;
}

async function startTestProxy(socketPath: string): Promise<{ proxy: TestProxy; stop: () => Promise<void> }> {
  const port = await getFreePort();
  const child: ChildProcessWithoutNullStreams = spawn(TESTPROXY_BIN, [String(port), socketPath], {
    stdio: 'pipe',
  });
  let spawnError: Error | null = null;
  child.once('error', (err) => {
    spawnError = err instanceof Error ? err : new Error(String(err));
  });

  await waitForListening(port, 5000).catch((e) => {
    throw spawnError ?? e;
  });

  const stop = () =>
    new Promise<void>((resolve) => {
      if (child.exitCode !== null) {
        resolve();
        return;
      }
      child.once('exit', () => resolve());
      child.kill('SIGTERM');
    });

  return { proxy: { port, baseURL: `http://127.0.0.1:${port}` }, stop };
}

// Serves mac/devm/Resources over plain HTTP so gui.html runs from a
// normal http:// origin (see the file-level comment for why file://
// doesn't sidestep CORS the way it might seem to).
async function startStaticServer(): Promise<{ baseURL: string; stop: () => Promise<void> }> {
  const port = await getFreePort();
  const server: Server = http.createServer((req, res) => {
    const reqPath = req.url === '/' || req.url === undefined ? '/gui.html' : req.url;
    const filePath = path.join(GUI_RESOURCES_DIR, path.normalize(reqPath).replace(/^(\.\.[/\\])+/, ''));
    try {
      res.writeHead(200);
      res.end(readFileSync(filePath));
    } catch {
      res.writeHead(404);
      res.end('not found');
    }
  });
  await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', resolve));
  const stop = () => new Promise<void>((resolve) => server.close(() => resolve()));
  return { baseURL: `http://127.0.0.1:${port}`, stop };
}

export async function fetchDaemonFingerprint(proxy: TestProxy): Promise<string> {
  const resp = await fetch(`${proxy.baseURL}/version`);
  if (!resp.ok) {
    throw new Error(`GET ${proxy.baseURL}/version returned ${resp.status}`);
  }
  const body = (await resp.json()) as { fingerprint?: string };
  if (!body.fingerprint) {
    throw new Error('daemon /version response had no fingerprint field');
  }
  return body.fingerprint;
}

export interface OpenStatusPageOptions {
  proxy: TestProxy;
  /** Sets window.__DEVM_APP_FINGERPRINT__; omit to leave it unset. */
  fingerprint?: string;
}

export async function openStatusPage(page: Page, staticBaseURL: string, opts: OpenStatusPageOptions): Promise<void> {
  await page.addInitScript(
    ({ apiBase, fingerprint }: { apiBase: string; fingerprint: string | undefined }) => {
      // Playwright evaluates this function inside the browser page, a
      // separate TS compilation unit that doesn't see
      // src/lib/window-globals.d.ts's ambient declarations.
      (window as unknown as Record<string, unknown>).__DEVM_API_BASE__ = apiBase;
      if (fingerprint !== undefined) {
        (window as unknown as Record<string, unknown>).__DEVM_APP_FINGERPRINT__ = fingerprint;
      }
    },
    { apiBase: opts.proxy.baseURL, fingerprint: opts.fingerprint },
  );
  await page.goto(`${staticBaseURL}/gui.html`);
}

interface Fixtures {
  staticBaseURL: string;
  // Named daemonProxy, not proxy — @playwright/test's TestOptions already
  // reserves "proxy" for the browser's own network-proxy config, and a
  // same-named custom fixture silently overrides it with the wrong shape
  // (browser.newContext then fails: "proxy.server: expected string, got
  // undefined").
  daemonProxy: TestProxy;
  brokenProxy: TestProxy;
}

export const test = base.extend<Fixtures>({
  staticBaseURL: async ({}, use) => {
    assertPreconditions();
    const { baseURL, stop } = await startStaticServer();
    await use(baseURL);
    await stop();
  },

  daemonProxy: async ({}, use) => {
    assertE2eDaemonRunning();
    const { proxy, stop } = await startTestProxy(E2E_SOCKET);
    await use(proxy);
    await stop();
  },

  brokenProxy: async ({}, use) => {
    // Points at a socket path that is never created, so every request
    // this proxy forwards fails with a 502 — a deterministic stand-in
    // for "the daemon is unreachable" without a kill-mid-test race.
    const missingSocket = path.join(os.tmpdir(), `devm-testproxy-missing-${process.pid}.sock`);
    const { proxy, stop } = await startTestProxy(missingSocket);
    await use(proxy);
    await stop();
  },
});

export { expect };
