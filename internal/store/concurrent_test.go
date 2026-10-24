package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// The ingester and the kernel collector write at the same time. A write
// transaction that starts by reading (the chain head) and then writes must
// wait for the other writer, not fail with SQLITE_BUSY.
func TestConcurrentWritersDoNotCollide(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	const writers, each = 4, 150

	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				tx, err := st.Begin(ctx)
				if err != nil {
					errs <- err
					continue
				}
				if w%2 == 0 {
					_, err = tx.AppendKernel(ctx, KernelEvent{ExecID: fmt.Sprint(w), TS: "2026-10-17T09:00:00.000000000Z", Type: "file_unlink", Path: fmt.Sprint(i), Payload: `{}`})
				} else {
					err = tx.AddProcess(ctx, Process{ExecID: fmt.Sprintf("p%d-%d", w, i), Collector: "t", Binary: "b", StartedAt: "2026-10-17T09:00:00.000000000Z"})
				}
				if err != nil {
					tx.Rollback()
					errs <- err
					continue
				}
				if err := tx.Commit(); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("write failed: %v", err)
		break
	}
	if res, err := st.VerifyKernelChainFrom(ctx, ChainPoint{}); err != nil || res.BadID != 0 || res.Checked != (writers/2)*each {
		t.Errorf("chain after concurrent writes = %+v, %v", res, err)
	}
}
