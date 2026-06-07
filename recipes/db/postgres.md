---
name: tool/db/postgres
category: db
display_name: PostgreSQL
description: Postgres service inside the sandbox, port 5432.
keywords: postgres postgresql psql db database
since: recipes-v1.0.0
---

# PostgreSQL

## devm.yaml additions

```yaml
install:
  - apt-get install -y postgresql postgresql-contrib

services:
  postgres:
    port: 5432
    env_inject: true
    masks:
      - { path: var/lib/postgresql, size: 5G }
    startup:
      - command: [sudo, -u, postgres, /usr/lib/postgresql/16/bin/pg_ctl, start, -D, /var/lib/postgresql/16/main]
        background: true
```

## Notes

- The mask gives postgres a tmpfs-backed data dir; data does NOT
  survive sandbox teardown. Add a `mount` if you need persistence.
- `env_inject: true` exports `POSTGRES_PORT=5432` for other services
  in the sandbox.
- Postgres version may differ from 16 on the base image — adjust the
  startup path accordingly.
