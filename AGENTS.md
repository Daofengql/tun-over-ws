# AGENTS.md

## Overview

This file describes the project conventions for AI agents working on this codebase.

`wsvpn` is a centralized WebSocket L3 VPN prototype. Clients create TUN interfaces and send raw IPv4 packets over WebSocket to a Linux relay server. The current working path is overlay client-to-client forwarding; exit gateway is not implemented yet.

## Project Structure

```text
ws-vpn-go/
  bin/                  # Build output (gitignored)
  cmd/wsvpn/            # CLI entry point (cobra)
    main.go             # Root command, logger setup
    server.go           # Server subcommand
    client.go           # Client subcommand
  internal/
    config/             # YAML config parsing and validation
    conn/               # Client WS connection, heartbeat, reconnect, TUN pump
    logger/             # Colored zerolog setup
    packet/             # IPv4 packet parsing
    relay/              # Server relay, VIP allocator, forwarding, source validation
    tun/                # TUN device (wireguard-go), platform IP config
  testdata/             # Development config files
  scripts/              # Helper scripts (test-tun.ps1)
  docs/                 # Design, operations, roadmap, handoff documents
```

## Development Rules

1. Go code goes in `cmd/` or `internal/` only. Never put Go files in the repo root.
2. Platform-specific files use build tags through filename suffixes: `_windows.go`, `_linux.go`.
3. Tests use standard `_test.go` naming. Run with `go test ./...`.
4. Binary output goes to `bin/`. Never commit binaries or `wintun.dll`.
5. Config examples go in `testdata/`.
6. Use YAML only for config.
7. Keep the current MVP overlay-only unless the user explicitly asks for exit gateway work.
8. Do not treat UUID/token as production auth; they are test-stage fields and will be replaced later.

## Current Status

- Phase 0-2 complete: config, packet parsing, WebSocket relay, VIP allocation.
- Phase 3 complete: Windows and Linux TUN clients, overlay relay verified with ping.
- Phase 4 (exit mode): not started.
- Phase 5 (stability): heartbeat (30s), auto-reconnect with exponential backoff, source VIP validation, connection replacement.
- Phase 6 (security): only test token auth and source VIP validation exist. ACL and signed login are not implemented.

Verified scenarios:

- Windows single-machine two-client overlay ping.
- Remote Linux server + Linux client + Windows client overlay ping.
- Linux -> Windows: `ping -I 10.66.0.2 -c 4 -W 2 10.66.0.3`, 0% packet loss during the recorded test.

## Testing

Core checks:

```powershell
go test ./...
go vet ./...
go build -o .\bin\wsvpn.exe .\cmd\wsvpn
$env:GOOS = "linux"; $env:GOARCH = "amd64"; go build -o .\bin\wsvpn-linux-amd64 .\cmd\wsvpn
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

Local Windows integration test requires admin PowerShell:

```powershell
.\bin\wsvpn.exe server -c .\testdata\server.yaml --log-level debug
.\bin\wsvpn.exe client -c .\testdata\client-a.yaml --log-level debug
.\bin\wsvpn.exe client -c .\testdata\client-b.yaml --log-level debug
ping -S 10.66.0.2 10.66.0.3
```

Remote Windows/Linux overlay test uses explicit source/interface ping. Do not change default routes for the current MVP.

## Key Dependencies

- `github.com/coder/websocket` - WebSocket library
- `golang.zx2c4.com/wireguard/tun` - TUN device (cross-platform)
- `github.com/spf13/cobra` - CLI framework
- `github.com/rs/zerolog` - Structured logging
- `gopkg.in/yaml.v3` - YAML config

## Known Implementation Notes

- Linux TUN read/write must preserve headroom. `internal/tun/tun.go` uses `tunPacketOffset = 16`; do not remove it without retesting Linux packet writes.
- IPv6 packets currently show as debug noise and are dropped.
- `routes.exit.enabled` is not wired to default route changes.
- `--send-to` is a placeholder and does not currently inject a real packet.
