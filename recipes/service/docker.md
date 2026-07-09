---
name: tool/service/docker
category: service
display_name: Docker Engine
description: Docker Engine via get.docker.com. Sockets usable without sudo. Container egress transparently routed through devm's iron-proxy allow-list.
keywords: docker container runtime supabase compose
since: recipes-v1.0.0
---

# Docker Engine

Docker Engine installed inside the guest VM so container-based tools
(Supabase CLI, local databases, docker-compose stacks) work without
leaving the sandbox. devm's nftables rules transparently route
container HTTP/HTTPS/DNS through iron-proxy, so containers get the
same allow-list enforcement the guest does — nothing bypasses.

## devm.yaml additions

```yaml
install:
  # Docker Engine — the upstream installer sudo's internally.
  # `usermod -aG docker devm` puts our user in the docker group;
  # the group only takes effect on the NEXT login, hence the socket
  # override below (tart exec sessions don't re-resolve supplementary
  # groups, so within this VM lifetime `devm` still can't reach
  # /run/docker.sock without help).
  - curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker devm

  # Socket permissions drop-in. ExecStartPost=chmod 666 runs after
  # dockerd is up on every boot, so /run/docker.sock is world-writable
  # inside the VM (safe: no external process can reach it — the VM's
  # egress firewall + vmnet isolation make the docker socket
  # unreachable off-host).
  - |-
    sudo install -d /etc/systemd/system/docker.service.d && \
    printf '%s\n' '[Service]' 'ExecStartPost=/bin/chmod 666 /run/docker.sock' | \
      sudo tee /etc/systemd/system/docker.service.d/override.conf >/dev/null && \
    sudo systemctl daemon-reload && sudo systemctl restart docker

  # Host → container reachability. When a container publishes a port
  # (e.g., `-p 127.0.0.1:54322:5432`), Docker installs an OUTPUT-nat
  # DNAT that rewrites 127.0.0.1:54322 → 172.18.0.2:5432 (the
  # container's bridge IP). Our egress firewall's output chain runs
  # AFTER Docker's DNAT — it sees daddr in 172.x.x.x, doesn't match
  # any base accept, would drop. This rule opens all of Docker's
  # possible bridge subnets (default 172.17.0.0/16 plus user-defined
  # networks that go 172.18+) so published-port traffic reaches
  # containers.
  - sudo nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept

network:
  allow:
    # Docker Hub — required for `docker pull` on the default registry.
    - registry-1.docker.io
    - auth.docker.io
    - production.cloudfront.docker.com
```

## Notes

- **No `services:` entry for docker.** `get.docker.com` runs
  `systemctl enable --now docker.service` internally; declaring it as
  a devm service too causes devm's enable step to fail with "Unit
  docker.service has a bad unit file setting". Just install and let
  the upstream installer handle systemd wiring.

- **Container egress goes through iron-proxy.** You don't need to add
  anything for container network access — devm's nftables PREROUTING
  chain transparently DNATs container TCP:80/443 to iron-proxy and
  UDP:53 to the guest's dnsmasq. The container's `/etc/resolv.conf`
  still says `8.8.8.8` (Docker's default) but every DNS query lands
  at dnsmasq → iron-proxy anyway. To reach a hostname from a
  container, add it to `network.allow` above — same as any other
  workload.

- **Ports Docker leaves alone (SSH:22, custom APIs on non-80/443):**
  containers reaching those on the internet are blocked by devm's
  forward filter chain (default deny). If a specific container needs
  outbound on a non-standard port, add a rule to `user_forward`:
  ```yaml
  install:
    - sudo nft add rule inet devm_filter user_forward tcp dport 8080 accept
  ```
  Same escape-hatch pattern as `user_output` but on the forward hook.

- **Socket group hack.** `usermod -aG docker devm` alone isn't enough
  because supplementary groups are resolved at login and every
  `devm shell` opens a fresh tart-exec session that doesn't re-read
  the group list. The `chmod 666` drop-in makes it work regardless
  of group membership. Not a security concern — the docker socket is
  only reachable from inside the VM.

- **Published-port reachability on 80/443 is a known gap.** If you
  `docker run -p 127.0.0.1:80:80 nginx`, connecting to
  `curl http://127.0.0.1` from the guest hits devm's DNAT-to-
  iron-proxy first and never reaches the container (phantom 502).
  Common web dev use case; not fixed in this recipe (would need a
  `user_nat` chain, deferred until a real need surfaces). Publish on
  a non-80/443 port instead: `-p 127.0.0.1:8080:80`. Supabase
  (Postgres on 54322, Studio on 54323, etc.) isn't affected.

- **Docker Hub allow-list is minimal.** The three hosts above cover
  every plain `docker pull image:tag` from the default registry. Add
  more only for private registries (`ghcr.io`, `quay.io`,
  `<your-registry>.dkr.ecr.<region>.amazonaws.com`, ...).
