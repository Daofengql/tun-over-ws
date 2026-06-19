# CLAUDE.md

## Project

`wsvpn` is a centralized WebSocket L3 VPN in Go. Clients create TUN interfaces and send raw IPv4 packets to a relay server over WebSocket.

Current scope:

- Overlay client-to-client forwarding is implemented and tested on Windows/Linux.
- Exit mode is not implemented.
- UUID/token auth is test-stage only and will be replaced by server-signed login later.

## Build & Test

```powershell
go test ./...
go vet ./...
go build -o .\bin\wsvpn.exe .\cmd\wsvpn

$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o .\bin\wsvpn-linux-amd64 .\cmd\wsvpn
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

Do not commit `bin/`, binaries, or `wintun.dll`.

## Run (local Windows test, requires admin PowerShell)

```powershell
# Terminal 1: server
.\bin\wsvpn.exe server -c .\testdata\server.yaml --log-level debug

# Terminal 2: client A, usually 10.66.0.2
.\bin\wsvpn.exe client -c .\testdata\client-a.yaml --log-level debug

# Terminal 3: client B, usually 10.66.0.3
.\bin\wsvpn.exe client -c .\testdata\client-b.yaml --log-level debug

# Terminal 4: test
ping -S 10.66.0.2 10.66.0.3
```

## Run (remote Windows/Linux overlay test)

Known test topology:

```text
server: 47.250.198.120:27000
linux client vip: 10.66.0.2
windows client vip: 10.66.0.3
overlay: 10.66.0.0/24
```

Windows client:

```powershell
.\bin\wsvpn.exe client -c .\testdata\client-windows-remote.yaml --log-level debug
ping -S 10.66.0.3 10.66.0.2
```

Linux client:

```bash
./wsvpn-linux-amd64 client -c client-linux.yaml --log-level debug
ping -I 10.66.0.2 -c 4 -W 2 10.66.0.3
```

Do not change default routes for the current MVP. Use explicit ping source/interface.

## Architecture

- `cmd/wsvpn/` - Cobra CLI entry (server/client subcommands)
- `internal/packet/` - IPv4 header parsing and validation
- `internal/config/` - YAML config loading and validation
- `internal/relay/` - Server-side WebSocket relay, VIP allocator, source validation, forwarding
- `internal/conn/` - Client-side WebSocket connection, heartbeat, reconnection, TUN pump
- `internal/tun/` - TUN device wrapper (wireguard-go), platform-specific IP setup
- `internal/logger/` - Colored terminal logger (zerolog)

## Key Decisions

- Server: Linux-only target.
- Client: Windows + Linux.
- VIP allocation: server-issued (DHCP-like), client identified by UUID.
- WebSocket: `github.com/coder/websocket`, with 30s heartbeat ping and auto-reconnect.
- TUN: `golang.zx2c4.com/wireguard/tun`.
- Windows requires `wintun.dll` next to the binary.
- Linux TUN read/write uses 16-byte packet offset/headroom.
- Binary output goes to `bin/`.
- Overlay-only for now. Exit mode is a future phase.
- TLS is expected to be handled by reverse proxy for MVP.

## Conventions

- Go code in `cmd/` and `internal/` only. No `.go` files in root.
- Platform-specific code uses `_windows.go` / `_linux.go` suffixes.
- Config files in `testdata/` for development.
- Keep docs honest about what has been tested versus what is planned.

## Known Edges

- IPv6 packets are dropped with debug logs.
- `routes.exit.enabled` does not configure exit routes yet.
- Server TUN and NAT path are not implemented.
- Auth is not production-grade.
- `--send-to` is a placeholder.
