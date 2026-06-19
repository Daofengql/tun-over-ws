package conn

import (
	"sync"
	"time"
)

const (
	minCapacityBytesSec = 100 * 1024 // 100KB/s floor
	capacityHeadroom    = 0.8        // use 80% of measured capacity
	probeMultiplier     = 1.2        // probe recovery at 120%
	probeInterval       = 5 * time.Second
)

// RateLimiter implements a token bucket for congestion control.
//
// Deprecated: the connection pool now uses TCP backpressure through bounded
// queues. This type is kept temporarily for comparison and possible future
// diagnostics, but it is not used in the hot path.
type RateLimiter struct {
	mu sync.Mutex

	capacity float64 // bytes/sec, dynamic
	tokens   float64 // current available bytes
	lastTick time.Time

	// Recovery probing.
	lastProbe     time.Time
	probing       bool
	probeCapacity float64 // temporary expanded capacity during probe
}

// NewRateLimiter creates a rate limiter with the given initial capacity.
func NewRateLimiter(initialCapacity float64) *RateLimiter {
	if initialCapacity < minCapacityBytesSec {
		initialCapacity = minCapacityBytesSec
	}
	now := time.Now()
	return &RateLimiter{
		capacity:  initialCapacity,
		tokens:    initialCapacity, // start full
		lastTick:  now,
		lastProbe: now,
	}
}

// Allow checks whether n bytes can be sent. Consumes tokens if allowed.
func (rl *RateLimiter) Allow(n int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limit := rl.capacity
	if rl.probing {
		limit = rl.probeCapacity
	}

	// Refill tokens.
	now := time.Now()
	elapsed := now.Sub(rl.lastTick).Seconds()
	if elapsed > 0 {
		rl.tokens += limit * elapsed
		if rl.tokens > limit {
			rl.tokens = limit
		}
		rl.lastTick = now
	}

	if rl.tokens >= float64(n) {
		rl.tokens -= float64(n)
		return true
	}
	return false
}

// UpdateCapacity recalculates the bucket capacity based on aggregate connection throughput.
func (rl *RateLimiter) UpdateCapacity(conns []*ConnState) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var totalCurrent float64
	var totalPeak float64
	allThrottled := true
	for _, cs := range conns {
		totalCurrent += cs.CurrentThroughput()
		totalPeak += cs.PeakThroughput()
		if !cs.IsThrottled() {
			allThrottled = false
		}
	}

	newCap := rl.capacity
	if allThrottled {
		newCap = totalCurrent * capacityHeadroom
		if newCap < minCapacityBytesSec {
			newCap = minCapacityBytesSec
		}
	} else {
		if totalPeak > 0 {
			peakCap := totalPeak * capacityHeadroom
			if peakCap > newCap {
				newCap = peakCap
			}
		}
		if totalCurrent > 0 {
			demandCap := totalCurrent * probeMultiplier
			if demandCap > newCap {
				newCap = demandCap
			}
		}
	}

	// If probing and no further degradation, keep expanded capacity.
	if rl.probing && !allThrottled {
		rl.capacity = rl.probeCapacity
		rl.probing = false
	} else {
		rl.capacity = newCap
	}

	// Cap tokens to capacity.
	if rl.tokens > rl.capacity {
		rl.tokens = rl.capacity
	}

	// Start probe if all connections are throttled.
	if allThrottled && len(conns) > 0 && time.Since(rl.lastProbe) > probeInterval {
		rl.probing = true
		rl.probeCapacity = rl.capacity * probeMultiplier
		rl.lastProbe = time.Now()
	}
}

// Capacity returns the current rate limit in bytes/sec.
func (rl *RateLimiter) Capacity() float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.probing {
		return rl.probeCapacity
	}
	return rl.capacity
}
