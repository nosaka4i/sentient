package daemon

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// capture is the capture level sentientd runs at and the noise locations it
// leaves out (docs/capture-verbosity.md).
type capture struct {
	level policy.Verbosity
	noise []policy.NoisePath
}

// startCapture records the level in the store, warns when the installed
// Tetragon policy was generated for another level, and works out which
// locations count as noise: the ones this machine has plus the ones the
// installed policy lists, so the filter agrees with the policy.
func startCapture(ctx context.Context, st *store.Store, cfg Config, env policy.Env, now time.Time, log *slog.Logger) (*capture, error) {
	c := &capture{level: cfg.Verbosity}
	previous, err := st.RecordCaptureLevel(ctx, dbtime.Format(now), string(c.level), "flag")
	if err != nil {
		return nil, err
	}
	switch {
	case previous == "":
		log.Info("capture level", "level", c.level, "source", "flag")
	case previous != string(c.level):
		log.Info("capture level changed", "from", previous, "to", c.level, "source", "flag")
	default:
		log.Info("capture level", "level", c.level, "source", "flag")
	}

	c.noise = policy.DetectNoise(cfg.Homes, env)
	file := cfg.PolicyFile
	if file == "" {
		file = policy.DefaultPolicyFile
	}
	text, err := env.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("cannot read the installed policy", "file", file, "err", err)
		}
		return c, nil
	}
	theirs, listed, ok := policy.ParseHeader(string(text))
	if !ok {
		log.Warn("installed policy has no verbosity header", "file", file, "hint", "sen policy --verbosity "+string(c.level)+", install it, restart Tetragon")
		return c, nil
	}
	seen := map[string]bool{}
	for _, n := range c.noise {
		seen[n.Path] = true
	}
	for _, n := range listed {
		if !seen[n.Path] {
			c.noise = append(c.noise, n)
		}
	}
	if theirs != c.level {
		log.Warn("policy level differs", "policy", theirs, "sentientd", c.level,
			"hint", "sen policy --verbosity "+string(c.level)+", install it, restart Tetragon")
	}
	return c, nil
}
