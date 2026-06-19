# Connection Pool Implementation Plan

## Status

Phase 1-6 were implemented as an initial pool prototype. The current implementation has now changed the pool model from weighted multi-link dispatch plus token-bucket dropping to a fixed primary/standby WebSocket pool with backpressure-driven TCP pacing.

Unit tests cover packet classification, client-side pool enqueue behavior, primary promotion, connection-state diagnostics, and server-side TCP/UDP enqueue behavior. Real cross-machine overlay validation is still pending for the revised design.

## Design Goal

The client should keep a fixed-size WebSocket pool for one virtual node:

- One primary WebSocket carries normal traffic at any moment.
- One or more standby WebSockets stay connected and registered for fast failover, timeout rotation, and burst handling.
- The pool owns reconnection, standby rebuilding, primary promotion, and planned rotation.
- When the active WebSocket is CDN/nginx throttled, L4 applications behind the TUN should naturally slow down instead of the client reading unlimited TUN packets and dropping them in userspace.

This keeps the current "single binary, L3 over WebSocket, relay server" architecture. It does not adopt mihomo's one-local-TCP-connection-to-one-outbound-connection model.

## Backpressure Model

The revised model uses bounded blocking queues rather than a complex global token bucket.

Desired TCP pacing chain:

```text
CDN/nginx throttles WebSocket
-> websocket Write becomes slow or blocks
-> primary connection write queue fills
-> TCP packet enqueue blocks
-> TUN read loop slows or stops reading
-> OS/TUN queue applies pressure
-> inner TCP sessions reduce their send rate
```

Important details:

- Backpressure is best-effort because TUN behavior differs by OS. It may block, shrink effective receive windows, or eventually drop near the virtual interface. That is still preferable to arbitrary userspace TCP packet dropping.
- TCP packets should not be dropped just because the pool queue is full. They should normally wait.
- UDP packets may still be dropped or short-waited because UDP has no reliable congestion feedback.
- ICMP and local multicast/broadcast should not be allowed to clog the TCP path.

## Traffic Classes

Traffic classification is based on IPv4 protocol and destination type.

### TCP

TCP uses backpressure-first behavior:

- Prefer the current primary connection.
- If the primary is healthy but slow, block on its write queue so the TUN read loop slows down.
- Do not weighted-random individual TCP packets across all WebSockets.
- Avoid moving packets from an existing TCP flow between WebSockets unless the primary has failed. Packet reordering is more damaging than waiting.
- On primary failure, promote a standby and resume through the new primary. Inner TCP may retransmit lost packets naturally.

Future optional improvement:

- Track a lightweight flow table so new TCP flows can be assigned to a burst standby while old flows remain on the primary. This is optional and should not be required for the first revised implementation.

### UDP

UDP uses lossy bounded behavior:

- Prefer the primary connection while it has space.
- If the primary queue is full, UDP may use a standby as burst capacity.
- If no connection can accept quickly, drop UDP.
- Keep UDP queueing shallow to avoid stale datagrams.

### ICMP

ICMP is lightweight and diagnostic:

- Prefer the primary.
- Allow a short bounded wait.
- Drop if the pool is saturated.

### Multicast and Broadcast

Multicast and broadcast packets should be filtered or deprioritized:

- Drop or short-wait local noise such as mDNS, LLMNR, SSDP, IGMP unless a future feature explicitly supports it.
- Never let multicast/broadcast fill the primary queue.

## Pool Roles

Each WebSocket in the pool has one role:

```text
primary  - normal data path
standby  - registered hot spare, may be used for UDP/burst/failover
draining - old primary being phased out; no new normal traffic
dead     - closed or failed, waiting removal/rebuild
```

Role rules:

- Exactly one alive connection should be primary when the pool is healthy.
- Standbys are already connected and registered with the server, so failover does not require a new hello round trip.
- Draining connections should finish queued writes, then close or become dead.
- Dead connections are removed and replaced while respecting `maxTotal`.

Default sizing:

```text
max_primary = 1
max_total   = 3
```

This means one primary plus up to two standby connections.

## Primary Selection and Switching

Primary switching should be conservative.

Switch immediately when:

- Primary WebSocket read/write fails.
- Heartbeat fails.
- Primary age reaches the learned rotation interval.

Switch or build standby when:

- Primary queue remains high for a sustained interval.
- Primary write latency remains high for a sustained interval.
- CDN timeout detector predicts the current primary is near forced disconnect.

Do not switch on a single slow write or a single full queue sample. Use hysteresis to avoid thrashing.

Suggested signals:

- `writeLatencyEWMA`
- `writeQueueDepth`
- `writeQueueFullSince`
- `bytesWritten`
- `age`
- heartbeat state

The initial implementation can keep the existing throughput and timeout detectors as observability signals, but they should not be responsible for dropping TCP packets.

## Client Data Path

Revised client TUN-to-pool flow:

```text
read packet from TUN
parse IPv4 header
classify traffic

TCP:
  enqueue to primary with blocking semantics
  if primary dead, promote standby and retry
  if context canceled, stop

UDP:
  try primary non-blocking
  if full, try standby non-blocking or short-wait
  if still full, drop

ICMP:
  short-wait primary
  drop on timeout

multicast/broadcast/noise:
  drop or short-wait according to policy
```

This replaces:

```text
rateLimiter.Allow(packet)
weighted random connection selection
drop when selected writeCh is full
```

with:

```text
bounded queues
blocking TCP enqueue
lossy UDP enqueue
primary/standby promotion
```

## Server Data Path

The relay server already supports multiple connections per UUID/VIP. The revised server behavior should mirror client-side class handling when forwarding to a target client:

- Select the target client's primary connection for TCP by default.
- If the target primary is dead, promote one of the target standbys.
- For TCP, avoid dropping solely because the target write queue is full; block or wait with a long enough context to create backpressure toward the sender side.
- For UDP, allow non-blocking standby burst or drop.
- For ICMP, short-wait or drop.

Server-side backpressure matters because client A sending to client B should slow down when B's active WebSocket is throttled. If the server drops immediately, A's TUN side cannot receive a clean pressure signal.

## Connection State

Keep per-connection state, but refocus it from weighted scheduling to lifecycle and diagnostics.

Fields to keep or add:

- `role`
- `alive`
- `createdAt`
- `lastWriteAt`
- `lastReadAt`
- `bytesWritten`
- `bytesRead`
- `writeLatencyEWMA`
- `queueDepth`
- `queueFullSince`
- `throttled` or `degraded` as an advisory flag

The `ConnState` throughput sampler can remain, but the first revised implementation should not depend on token-bucket capacity calculations.

## Timeout and Rotation

Keep the timeout detector:

- Record connection lifetimes on disconnect.
- Detect repeated forced disconnects.
- Build a standby before the predicted cutoff.
- Promote the standby before the old primary is killed by the CDN/nginx.
- Move the old primary to draining or close it after promotion.

This is one of the main reasons to keep hot standbys.

## Congestion Control Direction

Retire the token-bucket packet dropper as the main congestion-control mechanism.

Why:

- It guesses capacity from observed throughput.
- It can drop inner TCP packets at arbitrary points.
- It adds complexity while bypassing the natural TUN/TCP feedback loop.

Replacement:

- Use bounded blocking queues for TCP.
- Use write latency and queue depth as degradation signals.
- Use standby connections for failover and optional UDP/burst handling.
- Keep explicit dropping for UDP, multicast/broadcast, malformed packets, and shutdown.

`internal/conn/ratelimit.go` can be removed later or kept temporarily behind a disabled flag until the revised pool is validated.

## Implementation Phases

### Phase A: Packet Classification ✅

Files:

- `internal/packet`
- `internal/conn/pool.go`
- `internal/relay/server.go`

Tasks:

- Expose IPv4 protocol from parsed packets.
- Add helpers for TCP, UDP, ICMP, multicast, and broadcast classification.
- Avoid repeated ad hoc parsing in client and server paths.

### Phase B: Pool Roles ✅

File: `internal/conn/pool.go`

Tasks:

- Add connection roles: primary, standby, draining, dead.
- Track a single primary pointer or primary connection ID.
- Build initial primary plus standby connections according to `maxTotal`.
- Ensure standby VIP matches the pool VIP.
- Promote standby on primary failure.
- Rebuild standby after promotion.

### Phase C: Backpressure Dispatch ✅

File: `internal/conn/pool.go`

Tasks:

- Replace weighted random packet dispatch with traffic-class dispatch.
- TCP enqueue blocks on the primary write queue.
- UDP enqueue is non-blocking or short-wait and may use standby.
- ICMP enqueue short-waits.
- Multicast/broadcast gets dropped or deprioritized.
- Remove token-bucket `Allow` from the hot path.

### Phase D: Server Primary/Standby Awareness ✅

File: `internal/relay/server.go`

Tasks:

- Pick inferred primary for TCP forwarding. The first alive connection for a VIP is treated as primary.
- Use blocking or longer-wait enqueue for TCP.
- Keep UDP lossy and allow standby burst.

Explicit client-to-server role synchronization is not implemented yet. The server infers primary as the first alive connection for that VIP.

### Phase E: Lifecycle and Metrics ✅

Files:

- `internal/conn/connstate.go`
- `internal/conn/timeout.go`
- `internal/relay/server.go`

Tasks:

- Queue pressure tracking is implemented for standby building.
- Write latency EWMA is implemented as a diagnostic signal.
- Per-connection read/write byte counters are implemented.
- `lastReadAt` and `lastWriteAt` are implemented for lifecycle diagnostics.
- Queue depth, queue capacity, and `queueFullSince` snapshots are implemented.
- Keep heartbeat per connection.
- Keep timeout rotation.
- Log primary promotion, standby rebuild, degraded primary, UDP drops, and TCP waits.

Current behavior:

- Queue pressure can trigger standby building after sustained high-water samples.
- Write latency EWMA is recorded around successful WebSocket writes.
- Read/write counters and timestamps are observability signals only.
- The pool does not yet rotate primary based on EWMA alone; that should be added only after local and cross-machine behavior is stable enough to define safe thresholds.

### Phase F: Tests ✅ / local

Tests to add or update:

- TCP enqueue blocks instead of dropping when primary queue is full. ✅
- UDP uses standby when primary queue is full. ✅
- Primary failure promotes standby. ✅
- Standby rebuild scheduling respects `maxTotal`. ✅
- Primary rotation promotes standby and marks the old primary as draining. ✅
- Timeout rotation triggers planned promotion. ✅
- Server forwarding does not immediately drop TCP on full target queue. ✅
- Connection-state counters, timestamps, queue snapshots, and write latency EWMA are covered by unit tests. ✅
- Existing unit tests pass. ✅

Still pending:

- Real Windows/Linux overlay ping tests for this revised pool. This is intentionally deferred and should not be run until explicitly requested.

## File Changes Summary

| File | Action |
|------|--------|
| `internal/packet` | Add traffic classification helpers |
| `internal/conn/pool.go` | Change weighted pool into primary/standby backpressure pool |
| `internal/conn/client.go` | Keep the old `Conn` API as a thin wrapper over `Pool` |
| `internal/conn/connstate.go` | Refocus metrics on lifecycle, queue, write latency |
| `internal/conn/timeout.go` | Keep for planned rotation |
| `internal/conn/ratelimit.go` | Deprecate or remove from hot path |
| `internal/relay/server.go` | Add TCP-aware forwarding wait and optional role awareness |
| `docs/ARCHITECTURE.md` | Update after implementation |
| `docs/ROADMAP.md` | Update after implementation |

## Acceptance Criteria

- The pool keeps exactly one primary under normal operation.
- Standby connections stay connected and can be promoted without creating a new TUN.
- When the primary WebSocket is slow, TCP enqueue waits instead of dropping packets immediately.
- UDP does not block TCP and can be dropped under pressure.
- Client and server both avoid immediate TCP drop on full queues.
- Existing Windows/Linux overlay ping still works. This real network check is intentionally deferred for now.
- No tracked config/testdata or remote connection information is added to the repo.
