# Bhatti — Architecture

## Naming

**Bhatti** (भट्टी) means furnace — the system that manages fire, provides
the environment where work happens.

**Lohar** (लोहार) means blacksmith — the one who works inside the bhatti.
The guest agent that runs as PID 1 inside every microVM.

```
bhatti    — the daemon + CLI. Orchestrates sandboxes, exposes the API.
lohar     — the guest agent. Runs inside each sandbox as PID 1.
sandbox   — a Firecracker microVM (or Docker container on macOS).
```

---

## System Diagram

```
┌───────────────────────────────────────────────────────────────────┐
│  Host  (Pi 5 / arm64 or x86_64 Linux / any KVM-capable host)     │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │  bhatti daemon  (bhatti serve)                              │  │
│  │                                                             │  │
│  │  ┌──────────┐  ┌───────────────┐  ┌──────────────────────┐  │  │
│  │  │ REST/WS  │  │ Engine        │  │ Store (SQLite)       │  │  │
│  │  │ API      │  │               │  │ sandboxes, secrets,  │  │  │
│  │  │ :8080    │──│ Create/Destroy│  │ templates, volumes,  │  │  │
│  │  │          │  │ Exec/Shell    │  │ FC state             │  │  │
│  │  │ Proxy    │  │ File ops      │  └──────────────────────┘  │  │
│  │  │ Thermal  │  │ Pause/Resume  │                            │  │
│  │  │ Manager  │  │ Snapshot/     │                            │  │
│  │  │          │  │   Restore     │                            │  │
│  │  └──────────┘  └──────┬────────┘                            │  │
│  │                        │ implements                         │  │
│  │             ┌──────────┴──────────┐                         │  │
│  │    ┌────────▼──────┐  ┌───────────▼──────────┐              │  │
│  │    │ Docker Engine │  │ Firecracker Engine   │              │  │
│  │    │ (macOS dev)   │  │ (Linux production)   │              │  │
│  │    └───────────────┘  └───────────┬──────────┘              │  │
│  └────────────────────────────────────┼────────────────────────┘  │
│                                       │ TCP over TAP              │
│  ┌────────────────────────────────────┼────────────────────────┐  │
│  │  Sandbox (Firecracker microVM)     │                        │  │
│  │  ┌──────────────────────────────┐  │                        │  │
│  │  │  vmlinux kernel              │  │                        │  │
│  │  │  rootfs.ext4    config.ext4  │  │                        │  │
│  │  │  vol-*.ext4 (volumes)        │  │                        │  │
│  │  │                              │  │                        │  │
│  │  │  ┌────────────────────────┐  │  │                        │  │
│  │  │  │  lohar (PID 1)         │◄─┤──┘                        │  │
│  │  │  │  TCP :1024 (control)   │  │                           │  │
│  │  │  │  TCP :1025 (forward)   │  │                           │  │
│  │  │  │  session registry      │  │                           │  │
│  │  │  │  file handlers         │  │                           │  │
│  │  │  │  scrollback buffers    │  │                           │  │
│  │  │  └────────────────────────┘  │                           │  │
│  │  │  user: lohar  /workspace     │                           │  │
│  │  └──────────────────────────────┘                           │  │
│  │  tapXXXXXXXX ─── brbhatti0 (bridge) ─── iptables NAT        │  │
│  └─────────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────┘
```

---

## Sandbox Lifecycle

### Consumer's view

Consumers see two operations: create and destroy. Everything between is
bhatti's job. A sandbox is always `"running"` from the API's perspective.

```
Create ──► sandbox exists (always "running") ──► Destroy
                    │         ▲
                    exec, file, tunnel, session
                    (always works — ensureHot is transparent)
```

### Thermal states (internal)

Bhatti manages three thermal states invisibly:

```
Hot ◄──~400µs──► Warm ◄──~50ms──► Cold
 ▲                                   │
 └──────── any operation ────────────┘
```

| State | FC process | vCPUs | Host RAM | Resume | When |
|---|---|---|---|---|---|
| Hot | alive | running | allocated | — | active use |
| Warm | alive | paused | allocated | ~400µs | idle <30min |
| Cold | dead | — | freed | ~50ms | idle >30min |

Transitions:
- **Hot → Warm**: no attached sessions + idle N seconds. `PATCH /vm {"state":"Paused"}`
- **Warm → Hot**: any operation. `PATCH /vm {"state":"Resumed"}`
- **Warm → Cold**: paused too long. Snapshot to disk, kill FC process.
- **Cold → Hot**: any operation. New FC process, load snapshot.

`ensureHot()` is called before every operation that needs the VM.
Metadata queries (status, list) don't wake the VM.

---

## Engine Interface

The actual Go interface (`pkg/engine/engine.go`):

```go
type Engine interface {
    Create(ctx, spec)           → SandboxInfo, error
    Destroy(ctx, id)            → error
    Stop(ctx, id)               → error           // snapshot (hot → cold)
    Start(ctx, id)              → error           // restore (cold → hot)
    Status(ctx, id)             → SandboxInfo, error
    List(ctx)                   → []SandboxInfo, error
    Exec(ctx, id, cmd)          → ExecResult, error
    Shell(ctx, id)              → TerminalConn, error
    ListeningPorts(ctx, id)     → []int, error
    Tunnel(ctx, id, port)       → ReadWriteCloser, error
}
```

The Firecracker engine additionally implements:

```go
// Thermal management (used by server.ThermalEngine interface)
Pause(ctx, id)               → error           // hot → warm
Resume(ctx, id)              → error           // warm → hot
EnsureHot(ctx, id)           → error           // any → hot
ThermalState(id)             → string
Activity(ctx, id)            → ActivityInfo, error

// File operations (used by server.FileEngine interface)
FileRead(ctx, id, path, w)   → int64, string, error
FileWrite(ctx, id, path, mode, size, r) → error
FileStat(ctx, id, path)      → FileInfo, error
FileList(ctx, id, path)      → []FileInfo, error

// Session listing
SessionList(ctx, id)         → []SessionInfo, error

// State persistence
VMState(id)                  → map[string]interface{}
RestoreVM(id, name, status, state)
```

---

## Wire Protocol

Binary framing over TCP (or vsock). All host↔lohar communication.

```
┌────────────────┬───────────┬──────────────────────┐
│ Length (4B BE) │ Type (1B) │ Payload (N bytes)    │
└────────────────┴───────────┴──────────────────────┘
Length = 1 + len(Payload).  Max frame: 1MB.
```

Frame types:

```
I/O:      STDIN (0x01)  STDOUT (0x02)  STDERR (0x03)
Control:  RESIZE (0x04) EXIT (0x05)    ERROR (0x06)   KILL (0x07)
Exec:     EXEC_REQ (0x10)
Auth:     AUTH (0x11)
Forward:  FWD_REQ (0x20)  FWD_RESP (0x21)
Sessions: EXEC_LIST_REQ (0x30)  EXEC_LIST_RESP (0x31)
          EXEC_KILL (0x32)      SESSION_INFO (0x33)
Activity: ACTIVITY_REQ (0x40)   ACTIVITY_RESP (0x41)
Files:    FILE_READ_REQ (0x50)  FILE_READ_RESP (0x51)
          FILE_WRITE_REQ (0x52) FILE_WRITE_RESP (0x53)
          FILE_STAT_REQ (0x54)  FILE_STAT_RESP (0x55)
          FILE_LS_REQ (0x56)    FILE_LS_RESP (0x57)
```

Ports:
- **1024** — control (exec, sessions, files, activity)
- **1025** — forward (port tunneling)

Connection model:
- Control: one connection per operation. AUTH first (if configured), then
  one request frame. Connection closes when the operation completes.
  Exception: attached TTY sessions keep the connection open.
- Forward: one connection per tunnel. FWD_REQ → FWD_RESP, then unframed
  bidirectional TCP relay.

### File operation protocol

**Read**: `FILE_READ_REQ` → `FILE_READ_RESP` (size, mode) → `STDOUT` frames → `EXIT`.
Rejects directories and non-regular files (prevents infinite reads on `/dev/urandom`).

**Write**: `FILE_WRITE_REQ` (path, mode, size) → `STDIN` frames → `FILE_WRITE_RESP`.
Atomic: writes to temp file, then renames. Readers never see partial content.
Rejects negative sizes (prevents silent data loss from missing Content-Length).

**Stat**: `FILE_STAT_REQ` → `FILE_STAT_RESP` (name, size, mode, is_dir, mtime).

**List**: `FILE_LS_REQ` → `FILE_LS_RESP` (JSON array of FileInfo).
Capped at 10,000 entries. Validates target is a directory.

---

## API Surface

Implemented routes:

```
POST   /sandboxes                              create (template or direct)
GET    /sandboxes                              list
GET    /sandboxes/:id                          get
DELETE /sandboxes/:id                          destroy
POST   /sandboxes/:id/stop                     snapshot to disk
POST   /sandboxes/:id/start                    resume from snapshot
POST   /sandboxes/:id/exec                     non-TTY exec
GET    /sandboxes/:id/ws                       WebSocket shell
GET    /sandboxes/:id/sessions                 list sessions
GET    /sandboxes/:id/ports                    listening ports
GET    /sandboxes/:id/files?path=...           read file
GET    /sandboxes/:id/files?path=...&ls=true   list directory
PUT    /sandboxes/:id/files?path=...           write file (Content-Length required)
HEAD   /sandboxes/:id/files?path=...           stat file
ANY    /sandboxes/:id/proxy/:port/*            reverse proxy (HTTP + WebSocket)

POST   /templates                              create template
GET    /templates                              list templates
GET    /templates/:id                          get template
DELETE /templates/:id                          delete template

POST   /secrets                                create/update secret
GET    /secrets                                list (names only)
DELETE /secrets/:name                          delete

POST   /volumes                                create volume
GET    /volumes                                list volumes
GET    /volumes/:name                          get volume
DELETE /volumes/:name                          delete volume

GET    /ports                                  all listening ports across sandboxes
```

Create sandbox — template or direct:
```json
// Direct (no template needed):
{"name": "dev", "cpus": 2, "memory_mb": 1024, "env": {"K": "V"}, "init": "npm i"}

// Template-based:
{"template_id": "abc123", "name": "dev"}
```

---

## CLI

Same binary as daemon. `bhatti serve` starts daemon, everything else is CLI.

```
bhatti serve                        start daemon

bhatti create [--name N] [--cpus C] [--memory M] [--env K=V,K=V] [--init CMD]
bhatti list | ls                    list sandboxes
bhatti destroy | rm <id|name>       destroy sandbox

bhatti exec <id|name> -- CMD...     run command (exit code forwarded)
bhatti shell | sh <id|name>         interactive shell (Ctrl+\ to detach)
bhatti ps <id|name>                 list sessions

bhatti file read <id|name> PATH     read file to stdout
bhatti file write <id|name> PATH    write file from stdin
bhatti file ls <id|name> PATH       list directory

bhatti secret set NAME VALUE
bhatti secret list
bhatti secret delete NAME
```

Name-to-ID resolution: all commands accept sandbox name or ID.
Config: `BHATTI_URL`, `BHATTI_TOKEN` env vars, or `~/.bhatti/config.yaml`.

---

## Disk Layout

```
/var/lib/bhatti/
├── config.yaml                   daemon config
├── state.db                      SQLite (sandboxes, templates, secrets, FC state)
├── age.key                       secret encryption key
├── id_ed25519 / .pub             SSH keypair
├── lohar                         guest agent binary
├── images/
│   ├── vmlinux-arm64             kernel (or vmlinux-amd64)
│   └── rootfs-base-arm64.ext4   base rootfs (or -amd64)
└── sandboxes/
    └── <id>/
        ├── rootfs.ext4           CoW copy of base rootfs
        ├── config.ext4           config drive (env, files, volumes, auth)
        ├── vol-<name>.ext4       volumes (if any)
        ├── firecracker.sock      FC API socket
        ├── vsock.sock            vsock UDS
        ├── mem.snap              memory snapshot (when cold)
        └── vm.snap               VM state snapshot (when cold)
```

---

## Deployment

```bash
# From source (recommended):
git clone https://github.com/sahil-shubham/bhatti.git
cd bhatti
sudo ./scripts/install.sh

# What install.sh does:
#   1. Detects architecture (aarch64/x86_64)
#   2. Installs Go if missing
#   3. Installs Firecracker if missing
#   4. Builds bhatti + lohar from source
#   5. Downloads kernel
#   6. Builds rootfs (Ubuntu 24.04 + Node.js + tools) — ~10min first time
#   7. Generates config.yaml with auth token
#   8. Installs systemd service (deploy/bhatti.service)
#   9. Starts daemon, waits for health check
```

Subsequent installs skip existing components (kernel, rootfs, config)
and update the binaries + lohar agent inside the rootfs.

---

## Key Design Decisions

**TCP over TAP for post-snapshot.** Vsock is broken after Firecracker
snapshot/restore. TCP over virtio-net works. Lohar listens on both;
after resume, the TCP client is used.

**No FC Go SDK.** Direct HTTP to FC's Unix socket API. ~20 lines of
helpers replace thousands of SDK lines.

**No systemd in guest.** Lohar IS init. Mounts, networking, PTYs,
processes — all deterministic. Boot to ready in ~3.5s.

**Guest IP via kernel `ip=`.** Network up before init runs. No DHCP.

**Pure Go SQLite.** `modernc.org/sqlite`. Cross-compile with CGO_ENABLED=0.

**Exec IS sessions.** No separate concept. Every exec gets a session ID.
TTY execs survive disconnect. Scrollback on reattach.

**Three-tier thermals.** Hot (running), Warm (paused, ~400µs resume),
Cold (snapshotted, ~50ms resume). Consumer never sees this.

**Atomic file writes.** Write to temp file, fsync, rename. Concurrent
readers always see complete content (old or new, never partial).

**Secrets via age + config drive.** Encrypted at rest, decrypted at
sandbox creation, injected as files or env vars.

**Single binary.** `bhatti serve` = daemon, `bhatti create` = CLI.
No separate CLI tool to install or version.

**Build from source install.** No pre-built binary downloads from
GitHub (repo is private). Clone + `install.sh` builds everything.
CI creates releases for when the repo goes public.
