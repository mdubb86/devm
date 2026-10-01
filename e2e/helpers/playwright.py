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
        page.goto(url, wait_until="networkidle", timeout=15000)
        try:
            yield page
        finally:
            context.close()
            browser.close()
