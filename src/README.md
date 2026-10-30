# lsh-utproxy
# Не проверял пока что на high-load, но вполне production-ready

TCP and UDP forward proxy with a **HAProxy-style configuration file**. One
static Go binary, no runtime dependencies, no cgo required to build. It is a
layer-4 forwarder: it accepts a socket, picks a backend server and pumps bytes
in both directions — RDP, SSH, SMTP, SMTP/IMAP, game and voice servers, DNS and
other UDP services.

| | |
|---|---|
| Operating systems | Linux (amd64, arm64, armv7, i386), Windows 10/11 and Windows Server (amd64, arm64), macOS (amd64, arm64) |
| Runs as | console process, **Windows service** (Service Control Manager), **systemd** unit |
| Config | HAProxy-like subset, one file, read once at startup |
| License | **AS IS**, no warranty — see [LICENSE](../LICENSE) |

Russian version of this document: [README.ru.md](README.ru.md).

---

## Table of contents

- [What it does](#what-it-does)
- [What it does not do](#what-it-does-not-do)
- [Installation layout](#installation-layout)
- [Quick start](#quick-start)
- [Command line](#command-line)
- [Running as a Windows service](#running-as-a-windows-service)
- [Running under systemd on Linux](#running-under-systemd-on-linux)
- [Configuration reference](#configuration-reference)
- [Timeouts, explained once](#timeouts-explained-once)
- [Health checks](#health-checks)
- [UDP specifics](#udp-specifics)
- [Logging](#logging)
- [Monitoring and typical log lines](#monitoring-and-typical-log-lines)
- [Build](#build)
- [Repository layout](#repository-layout)
- [Troubleshooting](#troubleshooting)
- [License](#license)

---

## What it does

- **TCP and UDP proxying**, `mode tcp` / `mode udp` per section.
- **HAProxy-like config**: `global`, `defaults`, `frontend`, `backend`,
  `listen`, `bind`, `server`, `balance`, `timeout`, `option`, `maxconn`,
  `check`, plus the UDP knobs.
- **Load balancing**: `roundrobin`, `leastconn` (weight-aware), `source`
  (sticky by client IP).
- **Access control per frontend / listen section**: `allow`, `deny` and `drop`
  lines with one CIDR, one IP address, `localhost` or `all` per line, evaluated
  in the order they appear in the file — the first rule that matches the source
  address decides, and the rest of the block is not looked at.
- **Health checks**: TCP connect probe per server, with `inter`, `rise`, `fall`;
  a checked-down server stops receiving traffic, `backup` servers take over only
  when every active server is down.
- **Connection limits**: process-wide `maxconn`, per-frontend `maxconn`,
  per-server `maxconn` — all accounted atomically, no overshoot under load.
- **Correct proxy semantics**: separate copy buffer per direction (no stream
  corruption), HAProxy timeout model, clean half-close on FIN, whole session
  aborted on a hard error, one retry to another backend server on a failed dial.
- **Production plumbing**: graceful shutdown with a drain window, listeners
  bound before the process drops privileges (Linux `chroot`/`user`/`group`),
  `RLIMIT_NOFILE` raised automatically, TCP keepalive on both sockets,
  `SO_REUSEPORT` on Linux, JSON or human log format to stdout/stderr/file,
  config validation mode (`-t`).

## What it does not do

These are not hidden bugs, they are simply not implemented:

- No TLS termination or initiator (`ssl` in `bind` is accepted by the parser and
  ignored). No HTTP/L7: no headers, no redirects, no `mode http`.
- No `acl` / `use_backend` decisions: they parse, they do not route.
- No stats socket or stats page. The counters exist in the process but are not
  exported anywhere yet.
- No PROXY protocol, no `send-proxy`, no `original-source`/transparent source
  (`source` balancing hashes the client address, it does not spoof it).
- No config reload while running: the file is read once. A "reload" on Linux is
  a graceful restart (see the systemd section).
- `daemon` is ignored (use systemd or a Windows service), `nbproc` only sets
  `GOMAXPROCS`, `stats socket` and the server `port` option are not used.

If you need those, HAProxy itself is the right tool. This project exists because
HAProxy has no UDP support and does not run natively on Windows.

---

## Installation layout

The default paths are baked into the binary per platform:

| | Windows | Linux / other Unix |
|---|---|---|
| install directory | `C:\Soft\lsh-utproxy` | `/opt/lsh-utproxy` |
| binary | `C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe` | `/opt/lsh-utproxy/bin/lsh-utproxy` |
| config | `C:\Soft\lsh-utproxy\config\lsh-utproxy.cfg` | `/opt/lsh-utproxy/config/lsh-utproxy.cfg` |
| logs | `C:\Soft\lsh-utproxy\logs\` | `/opt/lsh-utproxy/logs/` |

Recommended tree:

```
C:\Soft\lsh-utproxy\            /opt/lsh-utproxy\
  bin\lsh-utproxy.exe             bin/lsh-utproxy
  config\lsh-utproxy.cfg          config/lsh-utproxy.cfg
  logs\lsh-utproxy.log            logs/lsh-utproxy.log
```

The log directory is created automatically if it is missing.

---

## Quick start

### Linux

```bash
# build (Go 1.20+)
cd src
make build
sudo mkdir -p /opt/lsh-utproxy/{bin,config,logs}
sudo cp lsh-utproxy /opt/lsh-utproxy/bin/
sudo cp -n config/example.cfg /opt/lsh-utproxy/config/lsh-utproxy.cfg   # then edit it
sudo /opt/lsh-utproxy/bin/lsh-utproxy -t          # validate the config, then exit
sudo /opt/lsh-utproxy/bin/lsh-utproxy             # run in the foreground, Ctrl+C stops it
```

### Windows 10/11 (PowerShell)

```powershell
cd src
go build -o lsh-utproxy.exe .\cmd\lsh-utproxy
New-Item -ItemType Directory -Force C:\Soft\lsh-utproxy\bin, C:\Soft\lsh-utproxy\config, C:\Soft\lsh-utproxy\logs | Out-Null
Copy-Item .\lsh-utproxy.exe C:\Soft\lsh-utproxy\bin\
Copy-Item .\config\example.cfg C:\Soft\lsh-utproxy\config\lsh-utproxy.cfg   # then edit it
& C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe -t      # validate the config, then exit
& C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe         # run in the foreground, Ctrl+C stops it
```

Remember to change the `log` line in the config from `log stdout ...` to a file
path when running as a service or as a background daemon.

---

## Command line

```
lsh-utproxy [-c config] [-t] [-service] [-v]
```

| flag | meaning |
|---|---|
| `-c <file>`, `--config <file>` | config file. Default: `<install>/config/lsh-utproxy.cfg`, then `./config/lsh-utproxy.cfg`, then `./config/example.cfg` |
| `-t`, `--test` | parse the config, print a summary of frontends/backends/servers, exit `0` (ok) or `1` (error). Nothing is bound |
| `-service` | run as a Windows service; use this in the service `binPath`. On Linux it prints an error and exits |
| `-service-name <name>` | service name used to register with the SCM (default `lsh-utproxy`); must match the name created with `sc create` |
| `-v`, `--version` | print version, commit and build date |
| `-h`, `--help` | usage |

When the binary is started by the Service Control Manager it detects that by
itself and switches to service mode even without `-service`; passing the flag
explicitly is clearer and is what the install script does.

---

## Running as a Windows service

The program implements the service protocol (`RegisterServiceCtrlHandler` via
`golang.org/x/sys/windows/svc`), so the SCM sees a normal service: it starts, it
reports *Running*, it stops on **Stop**, and it flushes connections inside the
`grace shutdown` window.

### Option A — the install script (recommended)
## Я проверял вариант B, он работает

Open an **elevated** PowerShell ("Run as administrator"). If script execution is
restricted on the machine, launch it with `-ExecutionPolicy Bypass`:

```powershell
cd C:\path\to\lsh-utproxy\src
powershell -NoProfile -ExecutionPolicy Bypass -File .\deploy\install-service.ps1 `
    -ExePath .\dist\lsh-utproxy-windows-amd64.exe `
    -TcpPorts 33390 `
    -UdpPorts 5353 `
    -StartService
```

What it does:

1. creates `C:\Soft\lsh-utproxy\{bin,config,logs}`;
2. copies the binary to `C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe`;
3. writes a starter `config\lsh-utproxy.cfg` if none exists (it never overwrites
   your config);
4. registers the `lsh-utproxy` source in the Application event log;
5. creates/updates the service with
   `binPath= "...\lsh-utproxy.exe -service"`, `start= auto`;
6. sets automatic restart on failure: 5 s, then 10 s, then 60 s, counter reset
   after 24 h;
7. opens the inbound firewall ports you passed (`-TcpPorts`, `-UdpPorts`);
8. with `-Update` it stops a running service first and replaces the binary;
9. with `-StartService` it starts the service.

Add `-WhatIf` to any run to see what would happen without changing anything.

Uninstall:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\deploy\uninstall-service.ps1
# add -Purge to delete C:\Soft\lsh-utproxy together with config and logs
```

### Option B — by hand with sc.exe

```bat
mkdir C:\Soft\lsh-utproxy\bin C:\Soft\lsh-utproxy\config C:\Soft\lsh-utproxy\logs
copy lsh-utproxy-windows-amd64.exe C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe

sc.exe create  lsh-utproxy binPath= "C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe -service -c C:\Soft\lsh-utproxy\config\main.cfg" start= auto DisplayName= "lsh-utproxy TCP/UDP proxy"
sc.exe description lsh-utproxy "TCP/UDP proxy with a HAProxy-like configuration"
sc.exe failure   lsh-utproxy reset= 86400 actions= restart/5000/restart/10000/restart/60000

:: optional: startup/stop/error messages in the Application event log
powershell -Command "New-EventLog -LogName Application -Source lsh-utproxy"

sc.exe start lsh-utproxy
sc.exe query lsh-utproxy
```

Space after `key=` is mandatory in `sc` syntax, and the value after
`binPath=` needs its own quotes.

### Day-to-day operations

```powershell
Get-Service lsh-utproxy | Format-List *
Restart-Service lsh-utproxy           # config changes are applied on restart
Stop-Service lsh-utproxy              # sessions get "grace shutdown" seconds to finish
Get-EventLog -LogName Application -Source lsh-utproxy -Newest 20
Get-Content C:\Soft\lsh-utproxy\logs\lsh-utproxy.log -Tail 50 -Wait
```

Before restarting the service, validate the edited config:

```powershell
& C:\Soft\lsh-utproxy\bin\lsh-utproxy.exe -t
```

### Service account

The service runs as **LocalSystem** by default. To run it as a dedicated
account, use `services.msc` → *Log On*, or:

```bat
sc obj lsh-utproxy ".\lshproxy" "password"
```

Give that account "Log on as a service" and read/write permission on
`C:\Soft\lsh-utproxy`. The `chroot` / `user` / `group` directives in the config
are Linux-only: if they are set in a Windows run, the program refuses to start
and says so (that is intentional — a silently ignored security setting is worse).

Ports below 1024 need no special rights on Windows.

### Service fails to start

| symptom | cause and fix |
|---|---|
| service starts then stops immediately, event 7000/1062 | wrong binary path, or the config file cannot be read → run `lsh-utproxy.exe -t` manually as the same account |
| **error 1053** ("the service did not respond") | the binary was registered **without** `-service`, so it never told the SCM that it started |
| "cannot open log file ... logging to stderr" in the file/console log | the log path is not writable by the service account (a service has no console, so those messages are lost) — fix the path or use `log stdout` with the event log only |
| port is free but bind fails with "access denied" | the port is inside a Hyper-V/WinNAT reserved range: `netsh int ipv4 show excludedportrange protocol=tcp` |

---

## Running under systemd on Linux

```bash
sudo useradd -r -s /usr/sbin/nologin lshutproxy
sudo mkdir -p /opt/lsh-utproxy/{bin,config,logs}
sudo cp lsh-utproxy /opt/lsh-utproxy/bin/
sudo cp config/example.cfg /opt/lsh-utproxy/config/lsh-utproxy.cfg      # edit it
sudo chown -R lshutproxy:lshutproxy /opt/lsh-utproxy
sudo cp deploy/lsh-utproxy.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now lsh-utproxy
journalctl -u lsh-utproxy -f
```

The unit in [`deploy/lsh-utproxy.service`](deploy/lsh-utproxy.service):

- runs as `lshutproxy` with `CAP_NET_BIND_SERVICE`, so ports below 1024 work
  while the process itself is unprivileged;
- `KillSignal=SIGTERM`, `TimeoutStopSec=15`: the program stops accepting new
  connections, gives established sessions `grace shutdown` seconds, then exits;
- `LimitNOFILE=1048576`, and the program additionally raises its own soft limit
  toward the hard limit at startup;
- `ExecReload=/bin/kill -s HUP $MAINPID` — the process exits gracefully and
  systemd immediately starts it again (the config is not re-read in place);
- logs: use `log stdout` to land in the journal, or `log
  /opt/lsh-utproxy/logs/lsh-utproxy.log` for a file.

To change the configuration: edit the file, `lsh-utproxy -t`, then
`systemctl restart lsh-utproxy`.

`chroot`, `user` and `group` in the config are applied by the program itself
after all listeners are bound. With the systemd unit above you do not need them
(systemd already runs the process unprivileged); they are there for the case
when you start the binary as root.

---

## Configuration reference

### Syntax rules

- A line that starts with whitespace belongs to the section above it; a
  non-indented line starts a new section.
- One directive per line, with all its arguments. There is **no** `\` line
  continuation.
- `#` starts a comment (also after a value).
- `"..."` and `'...'` group arguments that contain spaces; inside quotes `\"`
  escapes a quote. A UTF-8 BOM at the start of the file is ignored (Notepad and
  PowerShell write one).
- Backslashes are literal, so Windows paths work as written:
  `log C:\Soft\lsh-utproxy\logs\lsh-utproxy.log`.
- Keywords are case-insensitive, names of frontends/backends/servers are not.
- Durations: `500ms`, `5s`, `30s`, `3600s`, `1h`, `2m`, `1d`. A bare number is
  seconds.
- An unknown directive is skipped with a warning in the log, a *misplaced* one
  is a hard error at startup.

### Sections

| section | meaning |
|---|---|
| `global` | process-wide: logging, `maxconn`, `maxprocs`, `grace shutdown`, privileges |
| `defaults` | values inherited by the sections below (`mode`, `balance`, timeouts, `option`, UDP knobs) |
| `frontend <name>` | the listening side; needs `default_backend` |
| `backend <name>` | the server pool |
| `listen <name>` | frontend and backend under one name — what a simple forwarder usually wants |

### global

| directive | default | meaning |
|---|---|---|
| `log <target> [format json\|raw] [level <lvl>]` | `stdout`, `raw`, `info` | `<target>` is `stdout`, `stderr` or a **file path** (opened in append mode, parent directory created) |
| `maxconn <n>` | `0` (unlimited) | concurrent connections process-wide; also used to size the file-descriptor limit on Linux |
| `maxprocs <n>` / `nbproc <n>` | `0` = all CPUs | `GOMAXPROCS` |
| `grace shutdown <t>` | `10s` | how long established connections may finish after a stop signal |
| `chroot <dir>` | off | Linux only, applied after binding |
| `user <name>`, `group <name>` | off | Linux only, applied after binding |
| `daemon` | ignored | use systemd / a Windows service instead |
| `stats socket <path>`, `stats timeout <t>` | reserved | not implemented |

### defaults / frontend / backend / listen

| directive | valid in | default | meaning |
|---|---|---|---|
| `mode tcp\|udp` | all | `tcp` | proxying mode of this section |
| `bind <addr>:<port>` | frontend, listen | – | one or more lines. Forms: `0.0.0.0:80`, `*:80`, `[::]:80`, `::80`, `:8080`, `host.name:3389`. The address must exist on the machine, otherwise the start fails |
| `default_backend <name>` | frontend | – | where to send traffic (`listen` implies its own name) |
| `balance roundrobin\|leastconn\|source` | backend, listen, defaults | `roundrobin` | see below |
| `maxconn <n>` | frontend, listen, backend | `0` | enforced per frontend (and per listen section) and per server; a standalone `backend maxconn` is parsed but not separately enforced — use `maxconn` on the `server` line or the frontend |
| `timeout connect <t>` | all | `5s` | dial to a backend server |
| `timeout client <t>` | all | `30s` | inactivity on the client side |
| `timeout server <t>` | all | `30s` | inactivity on the backend side |
| `timeout tunnel <t>` | all | unset → 1 h floor | inactivity in **either** direction; wins over the two above |
| `timeout idle <t>` | all | unset | project-specific fallback, applies to both directions |
| `timeout check <t>` | all | `2s` | health-check dial timeout |
| `option tcpka` / `option tcpkeepalive` | all | **on** | TCP keepalive on both sockets |
| `tcp-keepalive [idle <t>] [intvl <t>] [cnt <n>]` | defaults, backend, listen | `30s`, `5s`, `3` | keepalive tuning; `intvl`/`cnt` are Linux-only |
| `re-use-port` | **defaults only** | off | `SO_REUSEPORT`, Linux only (ignored with a warning on other systems) |
| `udp-session-ttl <t>` | defaults, backend, listen | `30s` | UDP session lifetime without traffic |
| `udp-max-sessions <n>` | defaults, backend, listen | `10000` | UDP sessions per frontend |
| `udp-buffer-size <n>` | defaults, backend, listen | `65536` | UDP datagram buffer |
| `acl`, `use_backend` | frontend | – | parsed, **not applied** |
| `allow`, `deny`, `drop` | frontend, listen | – | access rules, one address per line; see [Access rules](#access-rules-allow--deny--drop) |

### server line

```
server <name> <addr>:<port> [check] [backup] [weight <1-256>] [maxconn <n>] [inter <t>] [rise <n>] [fall <n>]
```

| option | meaning |
|---|---|
| `check` | enable the TCP connect health check for this server |
| `backup` | used only when no active server is up |
| `weight` | used by `leastconn` (`active/weight`); `roundrobin` ignores it |
| `maxconn` | concurrent connections to this server, reserved atomically |
| `inter` | probe interval, default `2s` |
| `rise` / `fall` | consecutive ok / failed probes before the server flips up / down, defaults 2 / 3 |

### Access rules: allow / deny / drop
## этот функционал тоже проверил, работает

A `frontend` or `listen` section may carry its own ordered access list, nginx
style, one address per line:

```
listen rdp
  mode tcp
  bind 0.0.0.0:33390
  allow all
  drop 173.24.5.0/24
  allow 192.168.1.0/24
  deny 10.12.13.0/24
  allow 10.20.0.0/16
  deny all
  server rdp1 10.9.9.3:3389
```

**The rules are evaluated in the order they are written, and the first rule
that matches the client address wins.** The lines below it are not evaluated for
that client. In the example above `allow all` is the first line, so every
address is allowed immediately and the rest of the block is dead code — write
the exceptions first and the wide rules last, as in nginx.

The address forms are:

| form | meaning |
|---|---|
| `all` | every source address, IPv4 and IPv6 |
| `173.24.5.0/24`, `10.20.0.0/16`, `2001:db8::/32` | a CIDR network (host bits are ignored, `173.24.5.136/24` is `173.24.5.0/24`) |
| `173.24.5.136`, `::1` | one address, i.e. `/32` or `/128` |
| `localhost` | loopback: `127.0.0.0/8` and `::1` |

The action:

| rule | TCP | UDP |
|---|---|---|
| `allow` | the session is proxied as usual | the datagram is forwarded |
| `deny` | the connection is **reset** (SO_LINGER 0), so the client sees "connection refused" at once — the iptables **REJECT** behaviour | the datagram is not forwarded |
| `drop` | the socket is closed with a plain FIN and nothing is sent back, so the client waits for its own timeout — the iptables **DROP** behaviour | the datagram is not forwarded |

- If **no rule matches**, the traffic is allowed: an access list that ends
  without a match lets the address through, like nginx. Add `deny all` at the
  end to make a block whitelist-only.
- The rule applies to the whole frontend, to every `bind` line in it, and both
  to IPv4 and IPv6 clients (an IPv4 network never matches an IPv6 address, and
  the reverse; `all` matches both).
- UDP: `deny` and `drop` behave the same for the packet — a userspace proxy
  cannot send the ICMP "port unreachable" that REJECT would need, so the client
  simply gets no answer. The action is recorded in the log line.
- Every blocked connection or datagram is counted in the `blocked` counter, and
  the log gets one line per five seconds per frontend naming the rule:
  `connection refused by access rule` / `udp datagram refused by access rule`.
- Several addresses after one keyword are accepted: `deny 10.1.0.0/16
  10.2.0.0/16` is two rules with the same action.
- The address is the one the socket reports. Behind a NAT or a proxy chain the
  client appears with the address of the last hop; there is no PROXY protocol
  support, so `allow`/`deny` see the peer address, not the original one.
- `allow`/`deny`/`drop` in a `backend`, `defaults` or `global` section is a
  startup error: a rule that is silently ignored is worse than a refused start.
- A bad address (`10.0.0.0/33`, `notanip`) is a startup error too, and `-t`
  prints the rules of every frontend in the order they will be applied.

A whitelist-only RDP frontend that only the office and one partner may reach:

```
listen rdp
  mode tcp
  bind 0.0.0.0:33390
  allow 10.9.9.0/24
  allow 192.168.1.0/24
  allow 203.0.113.7
  deny 212.23.1.0/24      # this scanner is out, listed in the middle for clarity
  deny all
  server rdp1 10.9.9.3:3389 check
```

---

### Ready-made files

- [`config/example.cfg`](config/example.cfg) — one TCP and one UDP service, commented.
- [`examples/full-example.cfg`](examples/full-example.cfg) — every supported directive, with comments.
- [`examples/rdp.cfg`](examples/rdp.cfg) — an RDP farm, the settings that matter.
- [`config/main.cfg`](config/main.cfg) — a production-shaped example (RDP + DNS).

---

## Timeouts, explained once

This is where most "the proxy drops my sessions" reports come from.

For each direction the inactivity timer is the **first** value that is
configured:

1. `timeout tunnel` — applied to both directions (an established tunnel with no
   traffic either way);
2. `timeout idle` — both directions;
3. `timeout client` for the client direction and `timeout server` for the
   backend direction;
4. nothing configured → **1 hour**.

So with

```
defaults
  timeout client 30s
  timeout server 30s
  timeout tunnel 3600s
```

an idle but established session survives one hour, and `timeout client/server`
only matter if you remove `timeout tunnel`. That is HAProxy's TCP-mode
behaviour, and it is what RDP, SSH and most long-lived protocols need: a locked
RDP screen sends nothing at all.

Other timers:

- `timeout connect` — the dial to the backend. On failure the proxy retries once
  with the next server the balancer picks (with `balance source` that is the
  same server), then closes the client connection.
- A single blocked **write** is given 10 s; if a peer stops reading, the session
  dies rather than hanging.
- `tcp-keepalive idle 30s intvl 5s cnt 3` (on Linux) makes the kernel notice a
  dead peer in roughly `idle + intvl*cnt` ≈ 45 s even if nothing is written —
  useful on RDP where NAT boxes kill silent flows.

A closed session is logged at `debug` level with byte counters, and an idle
timeout is logged at `info` with the timer that fired — which makes
"why did the session end?" a one-line answer.

---

## Health checks

- Type: TCP connect to the server address, closed immediately. Probe timeout is
  `timeout check` (2 s default).
- Per server: `check` enables it, `inter` sets the interval (honoured per
  server, first probes are staggered), `fall` consecutive failures mark it down,
  `rise` consecutive successes mark it up.
- A checked-down server is removed from balancing right away; the log gets
  `server down` / `server up`.
- When **every** server in a backend is down, new connections are refused and
  the log says `no server available in this backend ...` at most once per five
  seconds per backend.
- For a UDP backend the "check" is a connect to an address, which always
  succeeds — it proves nothing. Do not rely on it; the session TTL is what
  recovers a dead UDP peer.

Health checks open a real TCP connection to your service. For RDP that is
usually fine, but keep `inter` at 10 s or more so the check traffic stays
trivial.

---

## UDP specifics

- A "session" is keyed by `frontend name + client IP:port`. The first datagram
  picks a backend server (per the `balance` algorithm, using the client IP for
  `source`) and opens a connected UDP socket toward it; replies are relayed back
  to the client's address.
- A session dies after `udp-session-ttl` (or `timeout idle` if that is the only
  one set) without traffic in either direction, and a garbage collector reaps
  them at most every 5 s.
- `udp-max-sessions` caps the table; over the cap, packets are dropped and the
  condition is logged once.
- UDP has no connection state to fail: there is no "connection dropped" here.
  If a backend goes away, the client's application retries at the protocol
  level.
- The reply path must be allowed by the firewall/NAT: the proxy sends replies
  from its own socket, so stateful firewalls on the proxy host must let the
  outbound socket see its traffic (they normally do).

---

## Logging

```
global
  log C:\Soft\lsh-utproxy\logs\lsh-utproxy.log format json level info
```

- levels: `trace`, `debug`, `info`, `warn`, `error`, `none`;
- formats: `json` (one JSON object per line) or `raw` (human-readable, no colour
  in a file);
- file targets are opened in append mode; the directory is created if missing;
- there is **no built-in log rotation**: use `logrotate` (Linux) or a scheduled
  `Copy-Item` + truncate / service restart (Windows). `logrotate` with
  `copytruncate` works without restarting the process;
- a Windows service additionally writes start/stop/fatal messages to the
  Application event log under the source `lsh-utproxy` (registered by the
  install script). Everything else goes to the log file, so a file target is
  what you want for a service.

---

## Monitoring and typical log lines

There is no stats endpoint yet. In the meantime:
```bash
ss -tanp | grep lsh-utproxy            # Linux: established sessions per listener
ss -s                                   # totals
ss -au                                  # Linux: UDP "connections" (sessions)
Get-NetTCPConnection -OwningProcess (Get-Process lsh-utproxy).Id   # Windows
```

Lines you should be able to recognize:

| line | meaning |
|---|---|
| `listening` (per bind) | the socket is bound and serving |
| `lsh-utproxy started` | all listeners bound, serving started |
| `server down` / `server up` | health-check state change |
| `backend dial failed` | TCP connect to a backend server failed (a retry follows) |
| `no server available in this backend ... connection refused` | every server in that backend is down |
| `frontend maxconn reached, connection refused` (debug) | the limit tripped, the socket was closed |
| `connection refused by access rule` | an `allow`/`deny`/`drop` rule blocked the client; the line names the rule and the source address (`deny` resets, `drop` closes silently) |
| `udp datagram refused by access rule` | the same for a UDP packet |
| `access rules loaded, first matching rule wins` | the frontend has a non-empty access list |
| `session closed by inactivity timeout` | an idle timer killed the session; it shows which one |
| `tcp proxy err (client -> backend)` | a real error mid-session (not a normal close) |
| `graceful shutdown deadline exceeded, closing remaining connections` | sessions did not finish inside `grace shutdown` |
| `re-use-port (SO_REUSEPORT) is not supported on this OS` | Windows/macOS build, a plain bind is used |

---

## Build

Requires Go 1.20 or newer (`go.mod` declares 1.20). Nothing else: no cgo, no C
toolchain.

```bash
cd src
make build            # current platform -> ./lsh-utproxy
make test             # go test ./...
make race             # go test -race ./... (needs cgo; run it on Linux)
make vet fmt tidy
make release          # linux amd64/arm64/armv7/i386 + windows amd64/arm64 -> dist/
make help             # list targets
```

The `Makefile` assumes a POSIX shell. On Windows build directly:

```powershell
go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o lsh-utproxy.exe .\cmd\lsh-utproxy
go test ./...
```

Cross-compile from any machine:

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/lsh-utproxy-windows-amd64.exe ./cmd/lsh-utproxy
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o dist/lsh-utproxy-linux-arm64     ./cmd/lsh-utproxy
```

Version, commit and build date are stamped through `-X main.version`,
`-X main.commit`, `-X main.date` and shown by `-v` and in the startup log line.

### Tests

`go test ./...` runs: config lexer/parser tests (including Windows paths, BOM,
misplaced directives, access rule order), the rule engine tests (first matching
rule wins, IPv4/IPv6, `all`, `localhost`, bad addresses), copy-engine tests
(stream integrity with simultaneous traffic in both directions, half-close,
hard-error teardown, per-direction idle timers), end-to-end TCP session tests
(banner + bulk both ways, `maxconn` accuracy, retry on a dead backend),
end-to-end access-rule tests for TCP (deny resets, drop closes silently) and UDP
(datagram discarded), health-check tests, and startup/shutdown tests that bind
real sockets and stop the process cleanly.

On Linux also run `go test -race ./...` — the race detector needs cgo and is not
available in a plain Windows build.

### CI

[`.github/workflows/build.yml`](../.github/workflows/build.yml) runs `gofmt`,
`go vet` and `go test -race` on Linux, `go vet` and `go test` on Windows, checks
the `-t` mode against the sample config on both, and uploads cross-compiled
binaries (`make release`) on push.

---

## Repository layout

```
src/                     the Go module
  cmd/lsh-utproxy/       main
  internal/config/       lexer, parser, config types
  internal/proxy/        TCP/UDP engines, balancing, health checks
  internal/balancer/     roundrobin, leastconn, source
  internal/util/         platform-specific helpers (build tags per file)
  internal/service/      Windows service integration
  internal/logging/      logger setup
  internal/stats/        counters
  config/, examples/     sample configurations
  deploy/                systemd unit, Windows service install/uninstall scripts
```

Platform differences live in files with build tags — `*_linux.go` and
`*_other.go` / `*_windows.go` — so the portable code contains no conditionals.

Before publishing the repository, drop artifacts that should not be in git:

```bash
git rm --cached src/cmd/lsh-utproxy/lsh-utproxy   # a committed Linux binary
```

---

## Troubleshooting

**RDP (or SSH/VPN) sessions drop after a while, but reconnect works.**
An idle timer fired. Set `timeout tunnel` long (for example `3600s`), and check
the log line `session closed by inactivity timeout`, which names the timer.
On Linux also make sure `option tcpka` is on and `tcp-keepalive` is set, because
intermediate NATs expire silent flows.

**Sessions break right after login / the picture freezes.**
A corrupted byte stream usually looks like this. If you hit it in an old build,
update — the two copy directions used to share one buffer. If it happens in the
current build, enable `level debug` and look at `tcp proxy err` lines and byte
counters.

**`bind ...: The requested address is not valid` / Windows "access denied".**
The address is not a local interface (check `ip addr` / `ipconfig`). On Windows,
also check reserved port ranges:
`netsh int ipv4 show excludedportrange protocol=tcp`.

**Windows service does not start.** See the table in the service section; error
1053 means the `-service` flag is missing in `binPath`.

**Linux: cannot bind port 80/443/53.** Start as root once, or keep the unit
unprivileged with `AmbientCapabilities=CAP_NET_BIND_SERVICE` as the provided
unit does.

**Too many open files (Linux).** The program raises its own soft limit, but the
hard limit is the ceiling: set `LimitNOFILE` in the unit (the provided one sets
1048576).

**Firewall.** On Windows, inbound rules are required for every forwarded port
(`install-service.ps1 -TcpPorts/-UdpPorts` does it, or
`New-NetFirewallRule -DisplayName "lsh-utproxy-in-tcp-33390" -Direction Inbound -Action Allow -Protocol TCP -LocalPort 33390`).

---

## Notes on behaviour worth knowing

- On a hard error in one direction the whole session is closed at once; a clean
  FIN from one side half-closes the other and the session continues until the
  tunnel/idle timer says otherwise (HAProxy's TCP semantics).
- Each direction has its own 64 KiB buffer, so a live TCP session costs about
  128 KiB of buffers plus socket buffers. On a machine with tens of thousands of
  concurrent sessions, size `maxconn` and the socket buffers accordingly.
- Balancing decisions are made per connection, not per packet; `source` sticks
  on the client address, which is what an RDP farm without shared profiles
  wants.

---

## License

**AS IS.** See [LICENSE](../LICENSE): you may use, copy, modify and distribute
it, and the software comes **without any warranty**, including fitness for any
particular purpose. The authors are not liable for anything it does or fails to
do. Run it in production at your own risk.
