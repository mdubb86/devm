# Third-party notices

devm bundles binaries and source from the following third-party projects.

## s6-log (s6-overlay)

The `s6-log` binary is included for Linux/arm64 and Linux/amd64 inside the
devm binary (embedded via `go:embed` at `internal/scripts/s6-log.linux-*`).
It's extracted unchanged from
[s6-overlay v3.2.0.2](https://github.com/just-containers/s6-overlay/releases/tag/v3.2.0.2)
and dropped into each sandbox at `.devm/scripts/s6-log` for the `wrap-bg.sh`
wrapper to use for rotated background-daemon log capture.

s6-overlay is licensed under the ISC License:

```
ISC License

Copyright (c) 2015-2024 The s6-overlay authors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```
