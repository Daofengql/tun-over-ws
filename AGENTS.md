# AGENTS.md

## Overview

This file describes the project conventions for AI agents working on this codebase.

## Project Structure

```
ws-vpn-go/
  bin/                  # Build output (gitignored)
  cmd/wsvpn/            # CLI entry point (cobra)
    main.go             # Root command, logger setup
    server.go           # Server subcommand
    client.go           # Client subcommand
  internal/
    config/             # YAML config parsing
    conn/               # Client WS connection, heartbeat, reconnect
    logger/             # Colored zerolog setup
    packet/             # IPv4 packet parsing
    relay/              # Server relay, VIP allocator, forwarding
    tun/                # TUN device (wireguard-go), platform IP config
  testdata/             # Test config files
  scripts/              # Helper scripts (test-tun.ps1)
  docs/                 # Design documents
```

## Development Rules

1. Go code goes in `cmd/` or `internal/` only. Never in the repo root.
2. Platform-specific files use build tags: `xxx_windows.go`, `xxx_linux.go`.
3. Tests use standard `_test.go` naming. Run with `go test ./...`.
4. Binary output goes to `bin/`. Never commit binaries.
5. Config examples go in `testdata/`.
6. No external runtime dependencies except `wintun.dll` on Windows.

## Current Status

- Phase 0-2 complete: config, packet parsing, WebSocket relay, VIP allocation.
- Phase 3 complete: Windows TUN client, single-machine relay verified with ping.
- Phase 4 (exit mode): not started.
- Phase 5 (stability): heartbeat (30s), auto-reconnect with exponential backoff implemented.
- Phase 6 (security): source IP validation in relay. Token auth. ACL not yet.

## Testing

- Unit tests: `go test ./internal/packet/ ./internal/relay/`
- Integration: use `scripts/test-tun.ps1` (requires admin PowerShell on Windows)
- Manual: start server, two clients, `ping -S <vip> <peer-vip>`

## Key Dependencies

- `github.com/coder/websocket` — WebSocket library
- `golang.zx2c4.com/wireguard/tun` — TUN device (cross-platform)
- `github.com/spf13/cobra` — CLI framework
- `github.com/rs/zerolog` — Structured logging
- `gopkg.in/yaml.v3` — YAML config
