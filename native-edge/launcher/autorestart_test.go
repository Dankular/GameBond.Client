package launcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedRunRelaunchesByItself(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := NewOwner()
	defer owner.Close()
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		Supervise(ctx, owner, func(c context.Context, ready func()) error {
			if runs.Add(1) == 1 {
				return errors.New("netstack crashed")
			}
			ready()
			<-c.Done()
			return nil
		}, Options{AutoRestart: true, Poll: 10 * time.Millisecond})
		close(done)
	}()
	deadline := time.Now().Add(6 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if runs.Load() < 2 {
		t.Fatal("a crashed tunnel was not relaunched")
	}
	cancel()
	<-done
}
