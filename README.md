# ⚒ Bhatti

Microvm sandbox infrastructure. Firecracker VMs with sub-millisecond pause/resume, persistent sessions, and transparent thermal management.

Built for running AI coding agents in isolated environments — each agent gets its own Linux VM with full filesystem, networking, and process isolation.

## Quick start

```bash
# On a Linux host with KVM (Raspberry Pi 5, AWS Graviton, x86_64 bare metal)
git clone https://github.com/sahil-shubham/bhatti.git
cd bhatti
sudo ./scripts/install.sh
```

The install script builds everything from source (~10 min on first run):

```
==> Installing bhatti on myhost (aarch64)
==> Installing Go 1.24.1...
==> Installing Firecracker 1.6.0...
==> Building bhatti and lohar from source...
==> Downloading kernel...
==> Building rootfs (this takes ~10 minutes on first install)...
==> Generating config...
==> Installing systemd service...
  waiting for daemon... ready

============================================
  bhatti is running on :8080
  auth token: a1b2c3d4e5f6...

  Quick start:
    export BHATTI_TOKEN=a1b2c3d4e5f6...
    bhatti create --name hello
    bhatti exec hello -- echo 'it works'
    bhatti shell hello
    bhatti destroy hello
============================================
```

## CLI

Single binary — `bhatti` is both the daemon (`bhatti serve`) and the CLI.

```bash
# Create a sandbox (no template needed)
bhatti create --name dev --cpus 2 --memory 1024
# → a1b2c3d4  dev  192.168.137.2

# Execute commands
bhatti exec dev -- uname -a
# → Linux dev 6.1.90 ... aarch64 GNU/Linux

bhatti exec dev -- node --version
# → v22.16.0

# Interactive shell (Ctrl+\ to detach)
bhatti shell dev

# File operations
echo 'console.log("hello")' | bhatti file write dev /workspace/app.js
bhatti file read dev /workspace/app.js
bhatti file ls dev /workspace/

# List sessions (init scripts, detached shells)
bhatti ps dev

# List all sandboxes
bhatti list

# Destroy
bhatti destroy dev

# Secrets
bhatti secret set API_KEY sk-...
bhatti secret list
bhatti secret delete API_KEY
```

Environment variables:
- `BHATTI_URL` — API endpoint (default: `http://localhost:8080`)
- `BHATTI_TOKEN` — Auth token (default: from `~/.bhatti/config.yaml`)

## REST API

```
POST   /sandboxes                              Create sandbox
GET    /sandboxes                              List sandboxes
GET    /sandboxes/:id                          Get sandbox
DELETE /sandboxes/:id                          Destroy sandbox
POST   /sandboxes/:id/exec                     Execute command
GET    /sandboxes/:id/ws                       WebSocket shell
POST   /sandboxes/:id/stop                     Snapshot to disk
POST   /sandboxes/:id/start                    Resume from snapshot
GET    /sandboxes/:id/ports                    List listening ports
GET    /sandboxes/:id/sessions                 List sessions
GET    /sandboxes/:id/files?path=...           Read file
GET    /sandboxes/:id/files?path=...&ls=true   List directory
PUT    /sandboxes/:id/files?path=...           Write file
HEAD   /sandboxes/:id/files?path=...           Stat file
GET    /sandboxes/:id/proxy/:port/...          HTTP reverse proxy
```

Create sandbox (no template required):
```json
POST /sandboxes
{
  "name": "my-sandbox",
  "cpus": 1,
  "memory_mb": 512,
  "env": {"API_KEY": "sk-..."},
  "init": "npm install",
  "new_volumes": [{"name": "work", "size_mb": 256, "mount": "/workspace"}]
}
```

## Performance

Measured on Raspberry Pi 5 (ARM64):

```
VM boot (create → ready):     ~3.5s
Exec roundtrip:               ~2.5ms
Pause  (hot → warm):          ~400µs
Resume (warm → hot):          ~400µs
Snapshot (hot → cold):        ~12s
Restore (cold → hot):         ~50ms
File write 10MB:              ~75ms
File read 10MB:               ~65ms
```

A paused sandbox resumes and executes a command in **under 3ms**.

## Architecture

```
bhatti (host daemon)
  ├─ CLI (create, exec, shell, file, secret, ...)
  ├─ REST/WS API (:8080)
  ├─ Firecracker engine (create, exec, snapshot/restore)
  ├─ Filesystem API (read, write, stat, ls — atomic writes)
  ├─ Thermal manager (hot → warm → cold, automatic)
  ├─ Bridge networking (192.168.137.0/24, up to 253 VMs)
  ├─ SQLite store (sandboxes, secrets, templates)
  └─ Reverse proxy (auto-forward detected ports)

lohar (guest agent, PID 1 inside each VM)
  ├─ TCP :1024 (exec, sessions, activity, file ops)
  ├─ TCP :1025 (port forwarding)
  ├─ Config drive (env vars, files, volumes, auth token)
  ├─ Session registry (TTY sessions survive disconnects)
  ├─ File handlers (atomic write via temp+rename)
  └─ Scrollback buffers (64KB ring buffer per session)
```

**Thermal states** — managed automatically:

| State | FC process | vCPUs | Host RAM | Resume time |
|-------|-----------|-------|----------|-------------|
| Hot   | alive     | running | allocated | —         |
| Warm  | alive     | paused  | allocated | ~400µs    |
| Cold  | dead      | —       | freed     | ~50ms     |

Idle sandbox → warm after 30s → cold after 30min. Any API request transparently wakes it.

## Key features

- **No templates required** — create sandboxes directly with CPUs, memory, env vars, init scripts. Templates still supported for repeated configurations.
- **Filesystem API** — read, write, stat, list files inside VMs. Writes are atomic (temp+rename) so concurrent readers never see partial content. Binary-safe, supports all 256 byte values.
- **Session-aware exec** — every TTY exec is a session. Disconnect and reconnect later; scrollback is replayed. Processes survive host disconnects.
- **Config drive** — hostname, env vars, secret files, DNS, volumes, init scripts — all injected at boot.
- **Auth tokens** — each VM gets a unique token. Both control and forward channels require it.
- **Volumes** — ext4 images attached as additional drives. Persist across snapshot/resume.
- **Init scripts** — run setup commands at boot as session ID `"init"`. Attachable for monitoring.
- **Bridge networking** — shared bridge, single masquerade rule. VMs can reach the internet and each other.

## Project structure

```
cmd/
  bhatti/
    main.go           daemon entrypoint (bhatti serve)
    cli.go            CLI commands (create, exec, shell, file, ...)
  lohar/
    handler.go        protocol dispatch (exec, sessions, files, activity)
    files.go          file read/write/stat/ls handlers (atomic writes)
    tty.go            TTY sessions, scrollback, detach/reattach
    session.go        session registry, ring buffer, idle timers
    exec.go           non-TTY piped exec
    forward.go        port forwarding relay
    main.go           PID 1 init: mounts, config drive, networking
pkg/
  agent/
    client.go         host-side client (exec, shell, sessions, files, forward)
    proto/            wire protocol (frame types, messages)
  engine/
    engine.go         sandbox lifecycle interface
    firecracker/
      engine.go       Firecracker implementation (create, pause, snapshot, files)
      network.go      bridge, TAP, IP pool
      configdrive.go  config drive + volume creation
  server/
    server.go         HTTP server, thermal manager
    routes.go         REST/WS handlers + file/session endpoints
    proxy.go          reverse proxy through VM tunnels
  store/
    store.go          SQLite (sandboxes, secrets, templates, FC state)
deploy/
  bhatti.service      systemd unit file
scripts/
  install.sh          full install from source (Go, Firecracker, rootfs, systemd)
  build-rootfs.sh     build base rootfs (Ubuntu 24.04 + Node + tools)
```

## Testing

80+ tests across three layers, all on real Firecracker VMs. Zero mocks.

```bash
# Agent-level tests (protocol handlers, no VM needed)
sudo go test -v -timeout=120s ./cmd/lohar/

# Integration tests (real Firecracker VMs)
sudo go test -v -timeout=600s ./pkg/engine/firecracker/

# CLI tests (full stack: CLI → HTTP → Firecracker → guest agent)
# Requires daemon running
sudo go test -v -timeout=300s ./cmd/bhatti/

# Cross-compile and run on remote Pi:
GOOS=linux GOARCH=arm64 go test -c -o bin/fc-test ./pkg/engine/firecracker
scp bin/fc-test pi:/tmp/ && ssh pi "sudo /tmp/fc-test -test.v"
```

Test coverage includes:
- VM lifecycle: create, exec, shell, snapshot/resume, destroy
- File operations: read, write, stat, ls, zero-byte, binary data, unicode filenames, permissions, atomic writes, concurrent access (10 parallel writers, same-file races)
- CLI: create, list, exec (success + failure + by-name), destroy, file write/read/ls, ps, secrets
- Networking: bridge, cross-VM, IP reuse, TAP cleanup
- Config drive: env vars, hostname, DNS, file injection
- Auth: token validation across channels
- Sessions: create/detach/reattach, scrollback, kill, idle timer
- Thermal: pause/resume, ensureHot from warm/cold
- Template-free creation: direct creation, defaults, env + init, volumes

## Requirements

- Linux (aarch64 or x86_64)
- KVM (`/dev/kvm`)
- Root access (for Firecracker, TAP devices, bridge networking)

Tested on:
- Raspberry Pi 5 (aarch64, Debian/Ubuntu)
- AWS Graviton bare metal (aarch64)

## License

Private project.
