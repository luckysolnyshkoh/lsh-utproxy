## Author Solnyshko Lucky Heartilly inc@luckysolnyshko.ru
# lsh-utproxy

TCP **and UDP** forward proxy with a HAProxy-style configuration file, written in
Go. One static binary, no dependencies, runs on **Linux** and **Windows 10/11**
(amd64/arm64): as a console process, as a **Windows service**, or under
**systemd**.

It exists because HAProxy has no UDP support and does not run natively on
Windows, while the day-to-day job — forward RDP, SSH, SMTP, DNS, game and voice
ports onto a couple of backend servers with health checks, connection limits and
clean shutdown — is the same in both cases.

```
listen rdp
  mode tcp
  bind 0.0.0.0:33390
  balance source
  timeout connect 5s
  timeout tunnel 3600s
  server rdp1 10.9.9.3:3389 check inter 10s fall 3 rise 2
  server rdp2 10.9.9.4:3389 check inter 10s fall 3 rise 2 backup

listen dns
  mode udp
  bind 0.0.0.0:5353
  balance roundrobin
  udp-session-ttl 40s
  server dns1 8.8.8.8:53
  server dns2 1.1.1.1:53
```

## Highlights

- TCP and UDP in one process, `mode tcp|udp` per section.
- HAProxy-like config: `global` / `defaults` / `frontend` / `backend` / `listen`,
  `bind`, `server ... check inter rise fall maxconn weight backup`,
  `balance roundrobin|leastconn|source`, `timeout connect|client|server|tunnel`,
  `maxconn`, `option tcpka`.
- Access control per frontend / listen section: `allow`, `deny`, `drop` lines
  with one CIDR, IP, `localhost` or `all` each, evaluated in the order written —
  the first matching rule wins. `deny` answers with a reset (REJECT), `drop`
  closes silently (DROP); the same rules work for UDP.
- Health checks that actually remove a dead backend from rotation; `backup`
  servers take over.
- Correct byte-stream semantics: a separate buffer per direction, HAProxy
  timeout model, clean half-close, whole-session abort on a hard error, one
  retry to another backend on a failed dial.
- Production plumbing: graceful shutdown with a drain window, per-process /
  per-frontend / per-server connection limits, TCP keepalive tuning, `SO_REUSEPORT`
  and privilege drop on Linux, `RLIMIT_NOFILE` raised at startup, JSON or plain
  logs to console or file, `-t` config validation mode.
- Native Windows service support (SCM protocol, event log, install/uninstall
  scripts) and a ready systemd unit.

## Where everything is

| | |
|---|---|
| **Full documentation** (features, config reference, Windows service, systemd, build, troubleshooting) | [`src/README.md`](src/README.md) |
| Go module and sources | [`src/`](src) |
| Sample configurations | [`src/config/example.cfg`](src/config/example.cfg), [`src/examples/`](src/examples) |
| systemd unit, Windows service scripts | [`src/deploy/`](src/deploy) |
| Default install directory | Windows `C:\Soft\lsh-utproxy`, Linux `/opt/lsh-utproxy` |

## Quick start
## btw, you better follow src/README.md

```bash
# Linux
cd src && make build && sudo ./lsh-utproxy -c config/example.cfg
```

```powershell
# Windows 10/11
cd src
go build -o lsh-utproxy.exe .\cmd\lsh-utproxy
.\lsh-utproxy.exe -t -c .\config\example.cfg      # check the config
.\lsh-utproxy.exe    -c .\config\example.cfg      # run in the console
```

As a Windows service (elevated PowerShell):

```powershell
cd src
powershell -NoProfile -ExecutionPolicy Bypass -File .\deploy\install-service.ps1 `
    -ExePath .\dist\lsh-utproxy-windows-amd64.exe -TcpPorts 33390 -StartService
```

## Status and license

Work in progress, but the proxying core, the tests and the deployment paths are
in use. **The software is provided "AS IS", without warranty of any kind** — see
[LICENSE](LICENSE).
