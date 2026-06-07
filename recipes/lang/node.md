---
name: tool/lang/node
category: lang
display_name: Node.js (npm)
description: Node 22 LTS with npm cache on the workspace mount.
keywords: node nodejs npm pnpm yarn package.json
since: recipes-v1.0.0
---

# Node.js

For projects with `package.json`.

## devm.yaml additions

```yaml
install:
  - curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
  - apt-get install -y nodejs

env:
  NPM_CONFIG_CACHE: $WORKSPACE/.npm-cache
```

## Notes

- npm cache on the workspace mount → fresh sandboxes hit cached
  packages on the host.
- Add `.npm-cache/` to `.gitignore`.
- For pnpm: `corepack enable && corepack prepare pnpm@latest --activate`
  in `install:`.
