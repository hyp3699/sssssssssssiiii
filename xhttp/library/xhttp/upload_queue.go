package xhttp

// uploadQueue is a specialized priorityqueue + channel to reorder generic
// packets by a sequence number. Packets may arrive out of order from
// concurrent POST requests; the queue buffers and reorders them before
// the server reads them in sequence.

import (
	"container/heap"
	"io"
	"sync"

	E "github.com/sagernet/sing/common/exceptions"
)

type packet struct {
	payload []byte
	seq     uint64
}

type uploadQueue struct {
	mu          sync.Mutex
	pushed      chan packet
	done        chan struct{} // closed by Close; never sent on
	heap        uploadHeap
	nextSeq     uint64
	closed      bool
	maxBuffered int
}

func newUploadQueue(maxBuffered int) *uploadQueue {
	if maxBuffered <= 0 {
		maxBuffered = 30
	}
	return &uploadQueue{
		pushed:      make(chan packet, maxBuffered),
		done:        make(chan struct{}),
		maxBuffered: maxBuffered,
	}
}

func (q *uploadQueue) Push(p packet) error {
	// Blocking send provides natural back-pressure: when the queue is full,
	// the server's ServeHTTP POST handler waits, which in turn flow-controls
	// the H2 stream, which back-pressures the client's Write().
	//
	// Selecting on done (instead of closing pushed in Close) is what makes
	// this race-free: a Push blocked here is released by Close, and no send
	// can ever land on a closed channel and panic. The previous
	// implementation checked closed under a mutex, unlocked, and then sent
	// on pushed — a concurrent Close closed pushed in between, panicking
	// with "send on closed channel".
	// Fast path: once Close has returned, Push fails deterministically
	// instead of racing the send case against the done case in select.
	select {
	case <-q.done:
		return E.New("upload queue closed")
	default:
	}
	select {
	case q.pushed <- p:
		return nil
	case <-q.done:
		return E.New("upload queue closed")
	}
}

func (q *uploadQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.done)
	return nil
}

// next returns the next pushed packet. It reports false once the queue is
// closed and no already-queued packet remains.
func (q *uploadQueue) next() (packet, bool) {
	select {
	case p := <-q.pushed:
		return p, true
	case <-q.done:
		// Drain anything queued before Close took effect.
		select {
		case p := <-q.pushed:
			return p, true
		default:
			return packet{}, false
		}
	}
}

// Read implements io.Reader. It returns reassembled bytes in seq order.
func (q *uploadQueue) Read(b []byte) (int, error) {
	for {
		if len(q.heap) == 0 {
			p, ok := q.next()
			if !ok {
				return 0, io.EOF
			}
			heap.Push(&q.heap, p)
		}
		p := heap.Pop(&q.heap).(packet)
		switch {
		case p.seq == q.nextSeq:
			n := copy(b, p.payload)
			if n < len(p.payload) {
				p.payload = p.payload[n:]
				heap.Push(&q.heap, p)
			} else {
				q.nextSeq = p.seq + 1
			}
			return n, nil
		case p.seq > q.nextSeq:
			if len(q.heap) > q.maxBuffered {
				return 0, E.New("reassembly buffer overflow")
			}
			heap.Push(&q.heap, p)
			next, ok := q.next()
			if !ok {
				return 0, io.EOF
			}
			heap.Push(&q.heap, next)
		default:
			// p.seq < nextSeq: stale duplicate, drop it and keep looking
			// for the next in-order packet. The previous implementation
			// fell out of the loop here and returned (0, nil), which
			// violates the io.Reader contract and can spin callers.
			continue
		}
	}
}

type uploadHeap []packet

func (h uploadHeap) Len() int           { return len(h) }
func (h uploadHeap) Less(i, j int) bool { return h[i].seq < h[j].seq }
func (h uploadHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *uploadHeap) Push(x any)        { *h = append(*h, x.(packet)) }
func (h *uploadHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }
