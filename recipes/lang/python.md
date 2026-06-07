---
name: tool/lang/python
category: lang
display_name: Python (uv)
description: uv-managed Python projects with sandbox-side cache.
keywords: python uv pyproject pip venv
since: recipes-v1.0.0
---

# Python (uv)

For projects with `pyproject.toml` or a uv lock.

## devm.yaml additions

```yaml
install:
  - curl -LsSf https://astral.sh/uv/install.sh | sh
  - cp /root/.cargo/bin/uv /usr/local/bin/uv 2>/dev/null || true

env:
  UV_CACHE_DIR: $WORKSPACE/.uv-cache
```

## Notes

- `UV_CACHE_DIR` lives on the workspace mount so it persists across
  sandbox teardown.
- Add `.uv-cache/` to `.gitignore`.
- If the project uses `pip` + `venv` instead of uv, skip the curl install
  and just rely on `apt-get install -y python3-pip python3-venv`.
