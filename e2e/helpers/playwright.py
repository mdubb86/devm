"""Playwright helper: thin context manager for headless Chromium.

Sole caller today is the filestash e2e — anywhere else that opens a
browser tab in a test uses this same shape so DOM-load timeouts stay
consistent.
"""
from __future__ import annotations
from contextlib import contextmanager
from typing import Iterator

from playwright.sync_api import Page, sync_playwright


@contextmanager
def open_page(url: str, *, ignore_https_errors: bool = False) -> Iterator[Page]:
    """Open url in a fresh headless Chromium page, yield the Page.

    ignore_https_errors defaults to False so devm's local CA trust
    is exercised — a test that turns it on has stopped proving the
    HTTPS path works.
    """
    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=True,
            args=["--disable-features=AsyncDns"],
        )
        context = browser.new_context(ignore_https_errors=ignore_https_errors)
        page = context.new_page()
        # Retry page.goto once on ERR_CONNECTION_RESET: Chromium's first
        # HTTPS handshake against devm's proxy is intermittently reset
        # (observed with --disable-features=AsyncDns in place; curl against
        # the same URL from the same shell works). A single retry after
        # 500ms clears it. `domcontentloaded` instead of `networkidle`
        # because filestash's SPA keeps polling, so networkidle can hang
        # on a healthy page.
        last_err = None
        for _ in range(2):
            try:
                page.goto(url, wait_until="domcontentloaded", timeout=15000)
                last_err = None
                break
            except Exception as e:
                last_err = e
                if "ERR_CONNECTION_RESET" not in str(e):
                    raise
                import time
                time.sleep(0.5)
        if last_err is not None:
            raise last_err
        try:
            yield page
        finally:
            context.close()
            browser.close()
