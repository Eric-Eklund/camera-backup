package copyop

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// chunkedReader hands out one small piece per Read, so a copy makes a known
// number of writes without moving enough data for the volume itself to affect
// the timing these tests measure. It is also closer to a real transfer than a
// bytes.Reader: the buffer bounds a read, it does not oblige the source to
// fill it, and a bytes.Reader would additionally offer WriteTo and let
// io.CopyBuffer hand over the whole stream in one write.
type chunkedReader struct {
	piece int
	left  int
}

func (c *chunkedReader) Read(b []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	c.left--
	n := c.piece
	if n > len(b) {
		n = len(b)
	}
	return n, nil
}

// chunks is a source that costs the destination n writes.
func chunks(n int) io.Reader { return &chunkedReader{piece: 1024, left: n} }

// steadyWriter accepts every chunk after a fixed pause. It is a slow link, not
// a broken one: it always makes progress.
type steadyWriter struct {
	perWrite time.Duration
	writes   atomic.Int64
}

func (s *steadyWriter) Write(p []byte) (int, error) {
	time.Sleep(s.perWrite)
	s.writes.Add(1)
	return len(p), nil
}
func (s *steadyWriter) Sync() error  { return nil }
func (s *steadyWriter) Close() error { return nil }

// The regression this fix is for. The timeout used to be a deadline for the
// whole file, so any transfer that honestly took longer than it was aborted —
// with the default of 60 s that is every video over a few hundred megabytes on
// a VPN, on a link that was working perfectly.
func TestCopyStream_SlowButSteadyDestinationIsNotAborted(t *testing.T) {
	dst := &steadyWriter{perWrite: 30 * time.Millisecond}

	start := time.Now()
	err := copyStream(dst, chunks(10), io.Discard,
		"BIG_0001.MOV", "/nas/BIG_0001.MOV", false,
		100*time.Millisecond, func() { t.Error("onAbandoned ran on a healthy copy") })
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("err = %v, want nil — the destination never stopped accepting data", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("the copy took %v; the test needs it to run well past the %v timeout to mean anything",
			elapsed, 100*time.Millisecond)
	}
	if got := dst.writes.Load(); got < 2 {
		t.Fatalf("destination saw %d writes; the test needs several to exercise the stall check", got)
	}
}

// The same guarantee on the verified path, which additionally calls Sync.
func TestCopyStream_SlowButSteadyVerifiedCopyIsNotAborted(t *testing.T) {
	dst := &steadyWriter{perWrite: 30 * time.Millisecond}

	err := copyStream(dst, chunks(8), io.Discard,
		"DSC_0001.NEF", "/nas/DSC_0001.NEF", true,
		100*time.Millisecond, func() { t.Error("onAbandoned ran on a healthy copy") })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// stallingWriter accepts a few chunks and then blocks for ever — a mount that
// drops partway through a file, which is the case the timeout exists for.
type stallingWriter struct {
	acceptFirst int
	seen        atomic.Int64
	release     chan struct{}
}

func (s *stallingWriter) Write(p []byte) (int, error) {
	if s.seen.Add(1) > int64(s.acceptFirst) {
		<-s.release
	}
	return len(p), nil
}
func (s *stallingWriter) Sync() error  { return nil }
func (s *stallingWriter) Close() error { return nil }

// Progress made before the stall must not buy the copy any extra grace, and it
// must not prevent the stall being noticed either: the clock runs from the last
// byte accepted, so the copy fails one timeout after the mount goes quiet.
func TestCopyStream_StallAfterProgressIsDetected(t *testing.T) {
	dst := &stallingWriter{acceptFirst: 3, release: make(chan struct{})}
	abandoned := make(chan struct{})
	const timeout = 100 * time.Millisecond

	start := time.Now()
	err := copyStream(dst, chunks(20), io.Discard,
		"BIG_0002.MOV", "/nas/BIG_0002.MOV", false, timeout, func() { close(abandoned) })
	elapsed := time.Since(start)

	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("err = %v, want errWriteTimeout", err)
	}
	// It should fire promptly after the stall, not after some multiple of the
	// timeout — the check interval is a quarter of it, so allow one over.
	if elapsed > 3*timeout {
		t.Errorf("took %v to notice a stall with a %v timeout", elapsed, timeout)
	}

	// Cleanup waits for the stuck write, exactly as on a hung mount.
	select {
	case <-abandoned:
		t.Fatal("onAbandoned ran while the write was still stalled")
	default:
	}
	close(dst.release)
	select {
	case <-abandoned:
	case <-time.After(5 * time.Second):
		t.Fatal("onAbandoned was not called after the stalled write returned")
	}
}

// A destination that never accepts anything at all is a stall from the start:
// the clock is armed before the first write, so there is no window in which a
// dead mount looks healthy.
func TestCopyStream_DestinationThatNeverAcceptsTimesOut(t *testing.T) {
	dst := &stallingWriter{acceptFirst: 0, release: make(chan struct{})}
	defer close(dst.release)

	err := copyStream(dst, chunks(4), io.Discard,
		"DSC_0002.NEF", "/nas/DSC_0002.NEF", false, 50*time.Millisecond, func() {})
	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("err = %v, want errWriteTimeout", err)
	}
}

// A timeout of zero or less disables the watchdog entirely — what a local
// Camera→SSD copy passes, where there is no network to hang.
func TestCopyStream_NoTimeoutNeverStalls(t *testing.T) {
	dst := &steadyWriter{perWrite: 20 * time.Millisecond}
	err := copyStream(dst, chunks(5), io.Discard, "DSC_0003.NEF", "/ssd/DSC_0003.NEF", false,
		0, func() { t.Error("onAbandoned ran with the timeout disabled") })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestStallCheckInterval(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"a quarter of the timeout", 4 * time.Second, time.Second},
		{"clamped up so a tiny timeout is not busy-polled", time.Millisecond, 10 * time.Millisecond},
		{"clamped down so a long timeout is still checked often", 10 * time.Minute, time.Second},
		{"the default 60s timeout", 60 * time.Second, time.Second},
		{"a short but sane timeout", 200 * time.Millisecond, 50 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stallCheckInterval(tc.timeout); got != tc.want {
				t.Errorf("stallCheckInterval(%v) = %v, want %v", tc.timeout, got, tc.want)
			}
		})
	}
}

// The stall clock and the progress sink are the same object, so a write that
// reaches the terminal bar is also what proves the destination is alive.
func TestStopWriter_MarksProgressAndStops(t *testing.T) {
	var sink strings.Builder
	sw := &stopWriter{w: &sink}
	sw.mark()

	if d := sw.idleFor(); d > time.Second {
		t.Fatalf("idleFor right after mark() = %v, want ~0", d)
	}

	if _, err := sw.Write([]byte("chunk")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sink.String() != "chunk" {
		t.Errorf("sink = %q, want the bytes forwarded", sink.String())
	}

	// Once stopped, an abandoned copy must not reach the sink again — it may
	// be a closed channel or a progress line another file now owns.
	sw.stopped.Store(true)
	if _, err := sw.Write([]byte("late")); err != nil {
		t.Fatalf("Write after stop: %v", err)
	}
	if sink.String() != "chunk" {
		t.Errorf("sink = %q, want the late write swallowed", sink.String())
	}
}

// The user-facing message has to name the fault correctly: "made no progress
// for 60s" tells someone to look at the mount, "timed out after 60s" tells
// them to raise the timeout, which was the wrong advice.
func TestNASTimeoutError_DescribesAStall(t *testing.T) {
	err := nasTimeoutError(60*time.Second, "BIG_0001.MOV", "/nas/2026/BIG_0001.MOV")
	msg := err.Error()
	for _, want := range []string{"made no progress", "1m0s", "BIG_0001.MOV", "/nas/2026/BIG_0001.MOV"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
}
