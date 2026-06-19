# Connection Pool Implementation Plan

## Status: Phase 1-6 Complete, Phase 7 (tests) Pending

## Overview

Implement a WebSocket connection pool for the client side with multi-connection support, QoS detection, weighted traffic distribution, and congestion control. Modify the relay server to support multiple connections per UUID.

## CDN Rate Limiting Model

- CDN/nginx typically rate-limits downstream (server → client direction)
- We treat it as symmetric (both directions) for simpler bandwidth calculation
- Client `bytesWritten` (upload) reflects the throttled direction
- Per-connection limiting: each connection is independently throttled

## Phase 1: Server Multi-Connection Support ✅

**File: `internal/relay/server.go`**

- Change `clients` from `map[string]*client` to `map[string][]*client`
- Change `vipMap` from `map[netip.Addr]*client` to `map[netip.Addr][]*client`
- `registerClient`: append to list (no replacement)
- `unregisterClient`: remove specific connection from list
- `forwardPacket`: select best connection by `len(WriteCh)` (lowest queue depth)
- Keep heartbeat per-connection (unchanged)
- Log total connection count per UUID on register/unregister

## Phase 2: ✅ Connection State Tracking

**File: `internal/conn/connstate.go`**

- `ConnState` struct per connection:
  - `peakThroughput` (float64, bytes/sec) - dynamic, window max
  - `currentThroughput` (float64, bytes/sec) - recent 2s average
  - `weight` (float64) - `current / peak`, used for traffic distribution
  - `throttled` (bool) - permanent once set, never resets
  - `createdAt` (time.Time)
  - `aliveDuration` (time.Duration) - for timeout detection
- Throughput tracking:
  - 200ms sampling buckets, 10s sliding window (50 buckets)
  - `RecordBytes(n int)` called by write loop
  - `Update()` called every 200ms, recalculates throughput and weight
- QoS detection:
  - 5s warmup period after connection creation
  - Condition: `peak > 1MB/s AND current < peak * 0.5`
  - Once `throttled = true`, never reverts
- Weight: `min(current/peak, 1.0)`, floor 0.1 for throttled connections

## Phase 3: ✅ Timeout Detector

**File: `internal/conn/timeout.go`**

- `TimeoutDetector`:
  - `samples []time.Duration` (last 5)
  - `detected time.Duration`
  - `configured time.Duration` (0 = auto-detect)
- `RecordDisconnect(duration)`:
  - Add to samples
  - If 3+ samples within ±5s of each other → set `detected`
  - `RotationInterval = detected * 0.8`
- `GetRotationInterval()`:
  - If configured > 0, return configured
  - If detected > 0, return detected * 0.8
  - Default: 50s

## Phase 4: ✅ Rate Limiter (Congestion Control)

**File: `internal/conn/ratelimit.go`**

- Token bucket:
  - `capacity` (float64, bytes) - dynamic, based on aggregate throughput
  - `tokens` (float64, bytes) - current available
  - `lastRefill` (time.Time)
- `UpdateCapacity(conns []*ConnState)`:
  - Sum all connections' `currentThroughput`
  - `capacity = sum * 0.8` (20% headroom)
  - Minimum capacity: 100KB/s (floor to avoid complete stall)
- `Allow(n int) bool`:
  - Refill tokens based on elapsed time × capacity
  - If tokens >= n: consume, return true
  - Else: return false (drop packet)
- `ProbeRecovery()`:
  - Called every 5s when all connections are throttled
  - Temporarily set capacity to 120%
  - If no further degradation detected → keep expanded
  - If degradation → revert

## Phase 5: ✅ Connection Pool

**File: `internal/conn/pool.go`**

- `Pool` struct:
  - `conns []*pooledConn` (wraps websocket.Conn + ConnState)
  - `active []*pooledConn` (connections registered with server)
  - `maxActive int` (default 2)
  - `maxTotal int` (default 3)
  - `timeoutDetector *TimeoutDetector`
  - `rateLimiter *RateLimiter`
  - `tunDev *tun.Device`
  - `serverURL, uuid, token string`
- Pool lifecycle:
  - `Connect(ctx)`: establish first connection, create TUN, register with server
  - `Run(ctx)`: main loop (TUN pump + pool manager + health monitor)
- TUN → Pool dispatch:
  - Read packet from TUN
  - `rateLimiter.Allow(len(packet))` check
  - If allowed: select connection by weight (weighted random)
  - If rejected: drop packet (triggers TCP backpressure)
- Connection selection:
  - Build weight array from all active connections
  - Weighted random selection (healthy connections naturally preferred)
  - Throttled connections receive overflow traffic
- Pool manager goroutine (runs every 1s):
  - Check rotation timer → if close to timeout, build new standby
  - Check QoS state of all connections → mark throttled
  - If active count < desired and standby available → activate
  - If active count < desired and no standby → build new
  - Close dead connections, clean up
  - Update rate limiter capacity
- Connection building:
  - `buildConn(ctx)`: WebSocket dial + hello handshake
  - Returns registered connection (sends hello to server)
  - Don't register with server until needed (to avoid replacing active)
- Graceful shutdown:
  - Close TUN
  - Close all WebSocket connections
  - Wait for goroutines

## Phase 6: ✅ Integration with Existing Client

**File: `internal/conn/client.go`**

- Refactor `Conn` to use `Pool` internally
- `Connect` → `pool.Connect`
- `Run` → `pool.Run`
- Keep backward-compatible API
- Remove old single-connection reconnect logic (pool handles it)

## Phase 7: (pending) Tests

**File: `internal/relay/server_test.go`**

- Test multi-connection registration: same UUID, 2 connections, both receive packets
- Test best-connection selection: one full queue, one empty → prefer empty
- Test unregister: remove one connection, other still works

**File: `internal/conn/connstate_test.go`**

- Test throughput tracking: record bytes, verify throughput calculation
- Test QoS detection: simulate throughput drop, verify throttled flag
- Test weight calculation: healthy vs throttled weights

**File: `internal/conn/ratelimit_test.go`**

- Test token bucket: allow/deny based on capacity
- Test capacity update: sum of connection throughputs
- Test probe recovery

## File Changes Summary

| File | Action |
|------|--------|
| `internal/relay/server.go` | Modify: multi-conn per UUID, best-path selection |
| `internal/conn/connstate.go` | New: throughput tracking, QoS detection, weights |
| `internal/conn/timeout.go` | New: auto-detect CDN timeout limits |
| `internal/conn/ratelimit.go` | New: token bucket congestion control |
| `internal/conn/pool.go` | New: connection pool manager |
| `internal/conn/client.go` | Modify: delegate to Pool |
| `internal/relay/server_test.go` | Modify: multi-conn tests |
| `internal/conn/connstate_test.go` | New |
| `internal/conn/ratelimit_test.go` | New |

## Implementation Order

1. Phase 1 (server) — independent, can be tested immediately
2. Phase 2 (connstate) — foundation for everything else
3. Phase 3 (timeout) — simple, standalone
4. Phase 4 (ratelimit) — depends on connstate concepts
5. Phase 5 (pool) — ties everything together
6. Phase 6 (integration) — wire into existing client
7. Phase 7 (tests) — throughout, but comprehensive pass at end
