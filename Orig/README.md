# lsh-utproxy

Production-ready TCP+UDP proxy with HAProxy-like configuration for Linux.

## Features

- **TCP + UDP proxy** — unified core, per-frontend/backend `mode tcp|udp`
- **HAProxy-like config** — subset of directives: `global`, `defaults`, `frontend`, `backend`, `listen`, `bind`, `server`, `balance`, `timeout`, `option`, `maxconn`, `check`
- **Load balancing** — `roundrobin`, `leastconn`, `source` (basic)
- **UDP flow tracking** — per `(src_ip:src_port + frontend)` session, idle TTL-based GC, automatic cleanup on timeout
- **Health checks** — TCP connect, UDP probe (best-effort), rise/fall, check intervals
- **High concurrency** — goroutine-per-connection, SO_REUSEPORT support, buffer pools
- **Production hardening** — timeouts, maxconn, graceful shutdown (SIGINT/SIGTERM), structured JSON/raw logs
- **Resource-aware** — GOMAXPROCS, large FD/connection limits ready

## Quick start

```bash
# Build
make build

# Create config
mkdir -p config
cp config/example.cfg config/lsh-utproxy.cfg

# Run
./lsh-utproxy -c config/lsh-utproxy.cfg
```

## Config syntax (highlights)

See `config/example.cfg`, `examples/*.cfg`. Key directives:

```cfg
frontend fe_udp
  mode udp
  bind 0.0.0.0:5353
  default_backend be_dns

backend be_dns
  mode udp
  balance roundrobin
  udp-session-ttl 30s
  udp-max-sessions 50000
  server dns1 8.8.8.8:53
  server dns2 1.1.1.1:53
```

## Build releases

```bash
make release  # linux-amd64, linux-arm64, linux-armv7
```

## Systemd install (Linux)

```bash
sudo mkdir -p /etc/lsh-utproxy
sudo cp config/example.cfg /etc/lsh-utproxy/lsh-utproxy.cfg
sudo cp lsh-utproxy /usr/local/bin/
sudo cp deploy/lsh-utproxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now lsh-utproxy
sudo journalctl -u lsh-utproxy -f
```

## Notes

- UDP: stateful by client endpoint per frontend. NAT/firewall must allow return path. Sessions expire by `timeout idle`/`udp-session-ttl`.
- Health checks: UDP healthcheck is lightweight (dial close). For some protocols better use passive + timeouts.
- `re-use-port` uses `SO_REUSEPORT` (Linux). On other OS behavior differs (ignored or no-op in this build focus).
- ACLs/use_backend partially parsed (placeholder for extension).

## License

MIT (or project default)