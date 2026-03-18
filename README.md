# ⚒ Bhatti

Microvm sandbox infrastructure. Firecracker VMs with sub-millisecond pause/resume, persistent sessions, and transparent thermal management.

Built for running AI coding agents in isolated environments — each agent gets its own Linux VM with full filesystem, networking, and process isolation.

## Performance

Measured on a1.metal (ARM64, Graviton 1):

```
VM boot (create → ready):     1.93s
Exec roundtrip:               2.5ms
Exec throughput:              876/sec
Pause  (hot → warm):          420µs
Resume (warm → hot):          370µs
Warm → first exec:            2.5ms
Snapshot (hot → cold):        11.7s
Restore (cold → hot):         46ms
```

A paused sandbox resumes and executes a command in **2.5ms**. Users don't notice.

## How it works

```
bhatti (host daemon)
  ├─ REST/WS API (:8080)
  ├─ Firecracker engine (create, exec, snapshot/restore)
  ├─ Thermal manager (hot → warm → cold, automatic)
  ├─ Bridge networking (192.168.137.0/24, up to 253 VMs)
  ├─ SQLite store (sandboxes, secrets, templates)
  └─ Reverse proxy (auto-forward detected ports)

lohar (guest agent, PID 1 inside each VM)
  ├─ TCP :1024 (exec, sessions, activity)
  ├─ TCP :1025 (port forwarding)
  ├─ Config drive (env vars, files, volumes, auth token)
  ├─ Session registry (TTY sessions survive disconnects)
  └─ Scrollback buffers (64KB ring buffer per session)
```

**Thermal states** — managed automatically, invisible to consumers:

| State | FC process | vCPUs | Host RAM | Resume time |
|-------|-----------|-------|----------|-------------|
| Hot   | alive     | running | allocated | —         |
| Warm  | alive     | paused  | allocated | ~400µs    |
| Cold  | dead      | —       | freed     | ~46ms     |

Idle sandbox with no attached sessions → warm after 30s → cold after 30min. Any API request transparently wakes it via `ensureHot()`.

## Key features

- **Session-aware exec** — every TTY exec is a session. Disconnect and reconnect later; scrollback is replayed. Processes survive host disconnects.
- **Config drive** — hostname, env vars, secret files, DNS, volumes, init scripts — all injected at boot via a 1MB ext4 image on `/dev/vdb`.
- **Auth tokens** — each VM gets a unique token. Both control and forward channels require it. Unauthenticated connections are rejected in <1ms.
- **Volumes** — ext4 images attached as additional drives (`/dev/vdc`, `/dev/vdd`, ...). Persist across snapshot/resume. Owned by the sandbox user.
- **Init scripts** — run setup commands at boot as session ID `"init"`. Attachable for monitoring. Runs as `lohar` (uid 1000) in `/workspace`.
- **Bridge networking** — shared bridge, single masquerade rule. VMs can ping each other. IP pool manages .2–.254 (253 VMs).
- **Age encryption** — secrets encrypted at rest with `age`. Decrypted at sandbox creation, injected via config drive.

## Setup

Requires an ARM64 Linux host with KVM (Raspberry Pi 5, AWS Graviton bare metal, etc).

```bash
# 1. Install Firecracker
curl -fsSL https://github.com/firecracker-microvm/firecracker/releases/download/v1.6.0/firecracker-v1.6.0-aarch64.tgz | tar xz
sudo mv release-v1.6.0-aarch64/firecracker-v1.6.0-aarch64 /usr/local/bin/firecracker

# 2. Download kernel
curl -fsSL 'https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/aarch64/kernels/vmlinux.bin' \
  -o /var/lib/bhatti/images/vmlinux-arm64

# 3. Build lohar (guest agent) and update rootfs
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/lohar-linux-arm64 ./cmd/lohar
sudo ./scripts/build-rootfs.sh bin/lohar-linux-arm64

# 4. Run bhatti
go build -o bhatti ./cmd/bhatti
sudo ./bhatti  # needs root for KVM, TAP devices, bridge
```

See [docs/pi-setup.md](docs/pi-setup.md) for full Raspberry Pi setup.

## Project structure

```
cmd/
  bhatti/           daemon + API server entrypoint
  lohar/            guest agent (PID 1 inside VMs)
    handler.go        protocol dispatch (exec, sessions, activity)
    tty.go            TTY sessions, scrollback, detach/reattach
    session.go        session registry, ring buffer, idle timers
    exec.go           non-TTY piped exec
    forward.go        port forwarding relay
    main.go           init: mounts, config drive, networking, listeners
pkg/
  agent/
    client.go         host-side client (exec, shell, sessions, forward)
    proto/            wire protocol (frame types, messages)
  engine/
    engine.go         sandbox lifecycle interface
    firecracker/
      engine.go         Firecracker implementation (create, pause, snapshot)
      network.go        bridge, TAP, IP pool
      configdrive.go    config drive + volume creation
  secrets/
    age.go            age encryption (key management, encrypt/decrypt)
  server/
    server.go         HTTP server, thermal manager, ensureHot
    routes.go         REST/WS handlers
    proxy.go          reverse proxy through VM tunnels
  store/
    store.go          SQLite (sandboxes, secrets, templates, FC state)
scripts/
  build-rootfs.sh     build base rootfs (Ubuntu 24.04 + Node + tools)
  test-on-pi.sh       cross-compile and run tests on Pi
```

## Testing

67 integration tests, all on real Firecracker VMs (a1.metal ARM64). Zero mocks.

```bash
# Run on a KVM-capable ARM64 host:
sudo go test -v -timeout=600s ./pkg/engine/firecracker/

# Cross-compile and run on remote host:
GOOS=linux GOARCH=arm64 go test -c -o bin/fc-test ./pkg/engine/firecracker
scp bin/fc-test host:/tmp/ && ssh host "sudo /tmp/fc-test -test.v"
```

Test coverage:
- VM lifecycle: create, exec, shell, snapshot/resume, destroy
- Bridge networking: cross-VM ping, cross-VM TCP, IP reuse, TAP cleanup
- Config drive: env vars, hostname, DNS, file injection, special characters
- Auth: no token, wrong token, correct token, forward channel, survives resume
- Volumes: single, multiple (3 devices), ownership, persist across resume
- Sessions: create/detach/reattach, scrollback, kill, idle timer, list
- Thermal: pause/resume, ensureHot from warm/cold, activity tracking
- Performance: boot time, exec latency, throughput, concurrent exec
- Error paths: double destroy, exec on stopped/warm/cold VM

## License

Private project.
