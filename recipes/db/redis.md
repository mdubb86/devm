---
name: tool/db/redis
category: db
display_name: Redis
description: Redis service inside the sandbox, port 6379.
keywords: redis cache kv
since: recipes-v1.0.0
---

# Redis

## devm.yaml additions

```yaml
install:
  - apt-get install -y redis-server

services:
  redis:
    port: 6379
    env_inject: true
    startup:
      - command: [redis-server, --bind, 0.0.0.0, --protected-mode, "no"]
        background: true
```

## Notes

- Default config — no persistence, no auth. Fine for dev. Don't
  expose port 6379 on the host without auth.
- `env_inject: true` exports `REDIS_PORT=6379` for other services.
