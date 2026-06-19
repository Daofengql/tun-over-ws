package conn

import (
	"testing"
	"time"
)

func TestConnStateRecordsReadWriteMetrics(t *testing.T) {
	cs := NewConnState()

	beforeWrite := time.Now()
	cs.RecordWrite(100, 10*time.Millisecond)
	cs.RecordWrite(50, 20*time.Millisecond)

	if got := cs.BytesWritten(); got != 150 {
		t.Fatalf("BytesWritten: got %d want 150", got)
	}
	if got := cs.LastWriteAt(); got.IsZero() || got.Before(beforeWrite) {
		t.Fatalf("LastWriteAt was not updated: %v", got)
	}
	if got := cs.WriteLatencyEWMA(); got < 11*time.Millisecond || got > 13*time.Millisecond {
		t.Fatalf("WriteLatencyEWMA: got %s want about 12ms", got)
	}

	beforeRead := time.Now()
	cs.RecordRead(42)

	if got := cs.BytesRead(); got != 42 {
		t.Fatalf("BytesRead: got %d want 42", got)
	}
	if got := cs.LastReadAt(); got.IsZero() || got.Before(beforeRead) {
		t.Fatalf("LastReadAt was not updated: %v", got)
	}
}

func TestConnStateRecordsQueueSnapshot(t *testing.T) {
	cs := NewConnState()

	cs.RecordQueue(1, 2)
	if got := cs.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth: got %d want 1", got)
	}
	if got := cs.QueueCapacity(); got != 2 {
		t.Fatalf("QueueCapacity: got %d want 2", got)
	}
	if got := cs.QueueFullSince(); !got.IsZero() {
		t.Fatalf("QueueFullSince: got %v want zero while queue is not full", got)
	}

	cs.RecordQueue(2, 2)
	fullSince := cs.QueueFullSince()
	if fullSince.IsZero() {
		t.Fatal("QueueFullSince was not set when queue became full")
	}

	cs.RecordQueue(2, 2)
	if got := cs.QueueFullSince(); !got.Equal(fullSince) {
		t.Fatalf("QueueFullSince changed while queue stayed full: got %v want %v", got, fullSince)
	}

	cs.RecordQueue(1, 2)
	if got := cs.QueueFullSince(); !got.IsZero() {
		t.Fatalf("QueueFullSince: got %v want zero after pressure cleared", got)
	}
}
