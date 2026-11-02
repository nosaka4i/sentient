package collector

import (
	"context"
	"sync"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

// FilterSink applies the capture level (docs/capture-verbosity.md) to what a
// collector reports: below Complete, opens of the noise locations are not
// stored. It is the safety net behind the policy, so a Tetragon policy more
// verbose than configured cannot grow the database. Everything that is not a
// file_open (exec, exit, network, writes, signals, gaps) always passes.
type FilterSink struct {
	Sink
	Level policy.Verbosity
	Noise []policy.NoisePath

	mu      sync.Mutex
	dropped map[string]int64
}

// Event implements Sink.
func (f *FilterSink) Event(ctx context.Context, e store.KernelEvent) error {
	if e.Type == "file_open" && f.Level != policy.Complete {
		if n, ok := policy.Match(f.Noise, e.Path); ok {
			f.mu.Lock()
			if f.dropped == nil {
				f.dropped = map[string]int64{}
			}
			f.dropped[n.Category]++
			f.mu.Unlock()
			return nil
		}
	}
	return f.Sink.Event(ctx, e)
}

// Dropped is how many opens were left out, by category, since the sink was
// made. (`extended` will store these as per-command summaries.)
func (f *FilterSink) Dropped() map[string]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int64, len(f.dropped))
	for k, v := range f.dropped {
		out[k] = v
	}
	return out
}
