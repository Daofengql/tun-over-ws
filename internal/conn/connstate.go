package conn

import (
	"sync"
	"time"
)

const (
	sampleInterval = 200 * time.Millisecond
	windowSize     = 10 * time.Second
	bucketCount    = int(windowSize / sampleInterval) // 50

	qosDegradationRatio = 0.5 // current < peak * 0.5 → throttled
	qosMinPeakBytesSec  = 1e6 // 1MB/s minimum peak to avoid idle false positives
	warmupDuration      = 5 * time.Second

	weightFloor = 0.1 // minimum weight for throttled connections

	writeLatencyEWMAAlpha = 0.2
)

// ConnState tracks per-connection throughput, lifecycle metrics, QoS health, and weight.
type ConnState struct {
	mu sync.Mutex

	// Throughput tracking.
	bytesInBucket int64
	buckets       [bucketCount]int64
	bucketIdx     int
	lastRotate    time.Time

	peakThroughput    float64 // bytes/sec, window max
	currentThroughput float64 // bytes/sec, recent 2s average

	// QoS.
	throttled bool // permanent once set
	degraded  bool // advisory lifecycle signal

	// Weight for traffic distribution.
	weight float64

	// Lifecycle and diagnostics.
	bytesWritten     int64
	bytesRead        int64
	lastWriteAt      time.Time
	lastReadAt       time.Time
	writeLatencyEWMA time.Duration
	queueDepth       int
	queueCapacity    int
	queueFullSince   time.Time
	degradedAt       time.Time
	degradedReason   string

	// Timing.
	createdAt time.Time
}

// NewConnState creates a new connection state tracker.
func NewConnState() *ConnState {
	now := time.Now()
	return &ConnState{
		createdAt:  now,
		lastRotate: now,
		weight:     1.0,
	}
}

// RecordBytes records bytes written to this connection.
func (cs *ConnState) RecordBytes(n int) {
	cs.RecordWrite(n, 0)
}

// RecordWrite records a successful WebSocket write and its observed latency.
func (cs *ConnState) RecordWrite(n int, latency time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now()
	if n > 0 {
		cs.bytesInBucket += int64(n)
		cs.bytesWritten += int64(n)
	}
	cs.lastWriteAt = now
	if latency > 0 {
		cs.writeLatencyEWMA = updateDurationEWMA(cs.writeLatencyEWMA, latency, writeLatencyEWMAAlpha)
	}
}

// RecordRead records bytes received from the WebSocket.
func (cs *ConnState) RecordRead(n int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if n > 0 {
		cs.bytesRead += int64(n)
	}
	cs.lastReadAt = time.Now()
}

// RecordQueue records the latest write queue depth snapshot.
func (cs *ConnState) RecordQueue(depth, capacity int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.queueDepth = depth
	cs.queueCapacity = capacity
	if capacity > 0 && depth >= capacity {
		if cs.queueFullSince.IsZero() {
			cs.queueFullSince = time.Now()
		}
	} else {
		cs.queueFullSince = time.Time{}
	}
}

// BytesWritten returns the total bytes successfully written to WebSocket.
func (cs *ConnState) BytesWritten() int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.bytesWritten
}

// BytesRead returns the total bytes read from WebSocket.
func (cs *ConnState) BytesRead() int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.bytesRead
}

// LastWriteAt returns the timestamp of the latest successful WebSocket write.
func (cs *ConnState) LastWriteAt() time.Time {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lastWriteAt
}

// LastReadAt returns the timestamp of the latest WebSocket read.
func (cs *ConnState) LastReadAt() time.Time {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lastReadAt
}

// WriteLatencyEWMA returns the smoothed WebSocket write latency.
func (cs *ConnState) WriteLatencyEWMA() time.Duration {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.writeLatencyEWMA
}

// QueueDepth returns the last observed write queue depth.
func (cs *ConnState) QueueDepth() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.queueDepth
}

// QueueCapacity returns the last observed write queue capacity.
func (cs *ConnState) QueueCapacity() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.queueCapacity
}

// QueueFullSince returns when the write queue became full, if it is still full.
func (cs *ConnState) QueueFullSince() time.Time {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.queueFullSince
}

// MarkDegraded marks the connection as degraded for lifecycle diagnostics.
func (cs *ConnState) MarkDegraded(reason string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.degraded {
		return
	}
	cs.degraded = true
	cs.degradedReason = reason
	cs.degradedAt = time.Now()
}

// IsDegraded returns whether this connection has been marked degraded.
func (cs *ConnState) IsDegraded() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.degraded
}

// DegradedReason returns the reason the connection was marked degraded.
func (cs *ConnState) DegradedReason() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.degradedReason
}

// DegradedAt returns when the connection was marked degraded.
func (cs *ConnState) DegradedAt() time.Time {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.degradedAt
}

func updateDurationEWMA(current, sample time.Duration, alpha float64) time.Duration {
	if current <= 0 {
		return sample
	}
	return time.Duration(float64(current)*(1-alpha) + float64(sample)*alpha)
}

// Update recalculates throughput, peak, weight, and QoS state.
// Should be called every sampleInterval (200ms).
func (cs *ConnState) Update() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(cs.lastRotate)
	if elapsed < sampleInterval {
		return
	}

	// Rotate bucket.
	cs.buckets[cs.bucketIdx] = cs.bytesInBucket
	cs.bucketIdx = (cs.bucketIdx + 1) % bucketCount
	cs.bytesInBucket = 0
	cs.lastRotate = now

	// Calculate current throughput (last 2s = 10 buckets).
	currentBytes := cs.sumRecentBuckets(10)
	currentDuration := 2.0
	if d := now.Sub(cs.createdAt).Seconds(); d < 2.0 {
		currentDuration = d
	}
	if currentDuration > 0 {
		cs.currentThroughput = float64(currentBytes) / currentDuration
	}

	// Calculate peak throughput (window max of 2s averages).
	totalBytes := cs.sumAllBuckets()
	windowDuration := now.Sub(cs.createdAt).Seconds()
	if windowDuration > windowSize.Seconds() {
		windowDuration = windowSize.Seconds()
	}
	if windowDuration > 0 {
		windowAvg := float64(totalBytes) / windowDuration
		if windowAvg > cs.peakThroughput {
			cs.peakThroughput = windowAvg
		}
	}

	// Weight: current / peak, floor for throttled.
	if cs.peakThroughput > 0 {
		cs.weight = cs.currentThroughput / cs.peakThroughput
		if cs.weight > 1.0 {
			cs.weight = 1.0
		}
	}
	if cs.throttled {
		if cs.weight < weightFloor {
			cs.weight = weightFloor
		}
	} else if cs.weight < 0 {
		cs.weight = 0
	}

	// QoS detection (skip during warmup).
	if !cs.throttled && now.Sub(cs.createdAt) > warmupDuration {
		if cs.peakThroughput > qosMinPeakBytesSec &&
			cs.currentThroughput < cs.peakThroughput*qosDegradationRatio {
			cs.throttled = true
		}
	}
}

// Weight returns the current traffic weight (0.0 to 1.0).
func (cs *ConnState) Weight() float64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.weight
}

// IsThrottled returns whether this connection has been QoS-degraded.
func (cs *ConnState) IsThrottled() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.throttled
}

// PeakThroughput returns the observed peak throughput in bytes/sec.
func (cs *ConnState) PeakThroughput() float64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.peakThroughput
}

// CurrentThroughput returns the recent throughput in bytes/sec.
func (cs *ConnState) CurrentThroughput() float64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.currentThroughput
}

// Age returns how long this connection has been alive.
func (cs *ConnState) Age() time.Duration {
	return time.Since(cs.createdAt)
}

// sumRecentBuckets sums the last n buckets (most recent).
func (cs *ConnState) sumRecentBuckets(n int) int64 {
	var sum int64
	idx := cs.bucketIdx
	for i := 0; i < n && i < bucketCount; i++ {
		idx = (idx - 1 + bucketCount) % bucketCount
		sum += cs.buckets[idx]
	}
	return sum
}

// sumAllBuckets sums all buckets in the window.
func (cs *ConnState) sumAllBuckets() int64 {
	var sum int64
	for _, b := range cs.buckets {
		sum += b
	}
	return sum
}
