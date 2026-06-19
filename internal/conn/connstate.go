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
)

// ConnState tracks per-connection throughput, QoS health, and weight.
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

	// Weight for traffic distribution.
	weight float64

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
	cs.mu.Lock()
	cs.bytesInBucket += int64(n)
	cs.mu.Unlock()
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
