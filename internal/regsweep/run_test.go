package regsweep

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// countingLister counts list calls; each one is one sweep.
type countingLister struct{ calls atomic.Int32 }

func (c *countingLister) ListRunners(context.Context) ([]Runner, error) {
	c.calls.Add(1)
	return nil, nil
}

func startRun(t *testing.T, interval time.Duration) (*Sweeper, *countingLister, context.CancelFunc) {
	t.Helper()
	l := &countingLister{}
	s, err := New(Config{Mode: ModeRemove, ScaleSetID: 1, Interval: interval}, l, &fakeScaleSet{}, &fakeLocal{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	return s, l, cancel
}

func wantSweeps(t *testing.T, l *countingLister, want int32, when string) {
	t.Helper()
	synctest.Wait()
	if got := l.calls.Load(); got != want {
		t.Fatalf("%s: %d sweeps, want %d", when, got, want)
	}
}

// The timer sweeps at startup and then once per interval, never more often.
func TestRunSweepsAtStartupAndOncePerInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, l, cancel := startRun(t, 10*time.Minute)
		defer cancel()
		wantSweeps(t, l, 1, "at startup")
		time.Sleep(10*time.Minute - time.Second)
		wantSweeps(t, l, 1, "just before the first interval")
		time.Sleep(time.Second)
		wantSweeps(t, l, 2, "at the first interval")
		time.Sleep(25 * time.Minute)
		wantSweeps(t, l, 4, "after 35 minutes")
	})
}

// A start failure triggers one sweep; a burst of triggers coalesces into one
// sweep no sooner than TriggerGap after the previous sweep started.
func TestRunTriggerSweepsOnceAndCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, l, cancel := startRun(t, 10*time.Minute)
		defer cancel()
		wantSweeps(t, l, 1, "at startup")

		time.Sleep(5 * time.Minute)
		s.Trigger()
		wantSweeps(t, l, 2, "trigger long after the last sweep")

		for range 5 {
			s.Trigger()
			time.Sleep(time.Second)
		}
		wantSweeps(t, l, 2, "burst inside the gap")
		time.Sleep(DefaultTriggerGap - 6*time.Second)
		wantSweeps(t, l, 2, "just before one gap after the triggered sweep")
		time.Sleep(time.Second)
		wantSweeps(t, l, 3, "one gap after the triggered sweep")
		time.Sleep(3*time.Minute + 59*time.Second)
		wantSweeps(t, l, 3, "no repeat without a new trigger")
		time.Sleep(time.Second)
		wantSweeps(t, l, 4, "the interval timer still runs")
	})
}

// Trigger never blocks, also before Run starts and while Run is not reading.
func TestTriggerNeverBlocks(t *testing.T) {
	s, err := New(Config{Mode: ModeLog, ScaleSetID: 1, Interval: time.Minute}, &countingLister{}, &fakeScaleSet{}, &fakeLocal{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		s.Trigger()
	}
}

func TestRunStopsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &countingLister{}
		s, _ := New(Config{Mode: ModeRemove, ScaleSetID: 1, Interval: time.Minute}, l, &fakeScaleSet{}, &fakeLocal{}, slog.New(slog.DiscardHandler))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()
		synctest.Wait()
		cancel()
		<-done
		before := l.calls.Load()
		time.Sleep(time.Hour)
		if l.calls.Load() != before {
			t.Fatal("swept after the context ended")
		}
	})
}
