package xhttp

import (
	"io"
	"sync"
	"testing"
	"time"
)

// Push racing Close must never panic with "send on closed channel", and a
// Push blocked on a full queue must be released by Close.
func TestUploadQueuePushCloseRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		q := newUploadQueue(1)
		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func(seq int) {
				defer wg.Done()
				_ = q.Push(packet{payload: []byte("x"), seq: uint64(seq)})
			}(j)
		}
		_ = q.Close()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("blocked Push was not released by Close")
		}
		// Push after Close returns an error instead of panicking.
		if err := q.Push(packet{payload: []byte("x"), seq: 99}); err == nil {
			t.Fatal("Push after Close: expected error, got nil")
		}
		// Close is idempotent.
		if err := q.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	}
}

func TestUploadQueueReadInOrder(t *testing.T) {
	q := newUploadQueue(30)
	go func() {
		_ = q.Push(packet{payload: []byte("cd"), seq: 1})
		_ = q.Push(packet{payload: []byte("ab"), seq: 0})
		_ = q.Push(packet{payload: []byte("ab"), seq: 0}) // duplicate of seq 0
		_ = q.Push(packet{payload: []byte("ef"), seq: 2})
		_ = q.Close()
	}()
	got, err := io.ReadAll(q)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef" {
		t.Fatalf("got %q, want %q", got, "abcdef")
	}
}

// rangeRand must always land inside [From, To].
func TestRangeRandBounds(t *testing.T) {
	for _, r := range []Range{{From: 0, To: 0}, {From: 5, To: 5}, {From: 100, To: 1000}, {From: 0, To: 1 << 20}} {
		for i := 0; i < 200; i++ {
			v := rangeRand(r)
			if v < r.From || v > r.To {
				t.Fatalf("rangeRand(%v) = %d, out of bounds", r, v)
			}
		}
	}
}
