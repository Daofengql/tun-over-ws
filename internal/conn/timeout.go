package conn

import (
	"math"
	"sync"
	"time"
)

const (
	maxSamples        = 5
	clusterTolerance  = 5 * time.Second
	minSamplesToDetect = 3
	rotationRatio     = 0.8
	defaultRotation   = 50 * time.Second
)

// TimeoutDetector auto-detects CDN/nginx connection time limits.
type TimeoutDetector struct {
	mu         sync.Mutex
	samples    []time.Duration
	detected   time.Duration
	configured time.Duration // 0 = auto-detect
}

// NewTimeoutDetector creates a detector. Set configured > 0 to skip auto-detection.
func NewTimeoutDetector(configured time.Duration) *TimeoutDetector {
	return &TimeoutDetector{
		configured: configured,
	}
}

// RecordDisconnect records how long a connection lived before it died.
func (td *TimeoutDetector) RecordDisconnect(aliveDuration time.Duration) {
	if aliveDuration < 5*time.Second {
		return // too short, probably a network error, not a timeout
	}

	td.mu.Lock()
	defer td.mu.Unlock()

	td.samples = append(td.samples, aliveDuration)
	if len(td.samples) > maxSamples {
		td.samples = td.samples[len(td.samples)-maxSamples:]
	}

	td.tryDetect()
}

// GetRotationInterval returns how long to wait before proactively rotating.
func (td *TimeoutDetector) GetRotationInterval() time.Duration {
	td.mu.Lock()
	defer td.mu.Unlock()

	if td.configured > 0 {
		return td.configured
	}
	if td.detected > 0 {
		return time.Duration(float64(td.detected) * rotationRatio)
	}
	return defaultRotation
}

// IsDetected returns whether a timeout limit has been auto-detected.
func (td *TimeoutDetector) IsDetected() bool {
	td.mu.Lock()
	defer td.mu.Unlock()
	return td.detected > 0
}

// tryDetect analyzes samples to find a consistent timeout pattern.
// Must be called with mu held.
func (td *TimeoutDetector) tryDetect() {
	if len(td.samples) < minSamplesToDetect {
		return
	}

	// Find the minimum sample as the likely timeout.
	minSample := td.samples[0]
	for _, s := range td.samples[1:] {
		if s < minSample {
			minSample = s
		}
	}

	// Count how many samples are within tolerance of the minimum.
	cluster := 0
	for _, s := range td.samples {
		if math.Abs(float64(s-minSample)) <= float64(clusterTolerance) {
			cluster++
		}
	}

	// If majority of samples cluster around the same value, it's a timeout.
	if cluster >= minSamplesToDetect {
		td.detected = minSample
	}
}
