# CLAUDE.md

## Project

`wsvpn` — A centralized WebSocket L3 VPN in Go. Clients create TUN interfaces, server relays IP packets between clients (overlay) or forwards to internet (exit mode, not yet implemented).

## Build & Test

```bash
go build -o bin/wsvpn.exe ./cmd/wsvpn/
go test ./...
go vet ./...
```

## Run (single-machine test, requires admin for TUN)

```bash
# Terminal 1: server
./bin/wsvpn.exe server -c testdata/server.yaml --log-level debug

# Terminal 2: client A
./bin/wsvpn.exe client -c testdata/client-a.yaml --log-level debug

# Terminal 3: client B
./bin/wsvpn.exe client -c testdata/client-b.yaml --log-level debug

# Terminal 4: test
ping -S 10.66.0.2 10.66.0.3 -n 4
```

## Architecture

- `cmd/wsvpn/` — Cobra CLI entry (server/client subcommands)
- `internal/packet/` — IPv4 header parsing
- `internal/config/` — YAML config loading and validation
- `internal/relay/` — Server-side WebSocket relay, VIP allocator, forwarding
- `internal/conn/` — Client-side WebSocket connection, heartbeat, reconnection
- `internal/tun/` — TUN device wrapper (wireguard-go), platform-specific IP setup
- `internal/logger/` — Colored terminal logger (zerolog)

## Key Decisions

- Server: Linux-only. Client: Windows + Linux.
- VIP allocation: server-issued (DHCP-like), client identified by UUID.
- WebSocket: `github.com/coder/websocket`, with 30s heartbeat ping and auto-reconnect.
- TUN: `golang.zx2c4.com/wireguard/tun`, Windows requires wintun.dll next to binary.
- Binary output goes to `bin/` directory.
- Overlay-only for now. Exit mode is a future phase.
- TLS handled by reverse proxy. Program runs ws:// only.

## Conventions

- Go code in `cmd/` and `internal/` only. No `.go` files in root.
- Platform-specific code uses `_windows.go` / `_linux.go` build tags.
- Config files in `testdata/` for development.
- `wintun.dll` not committed (copied from system or downloaded separately).
