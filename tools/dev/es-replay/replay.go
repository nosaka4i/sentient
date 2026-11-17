package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"
)

// Stats is what a replay did.
type Stats struct {
	Lines    int // capture lines read
	Sent     int // messages written (the hello counts)
	Skipped  int // lines that produced no message (closes, terminal writes)
	Unusable int // lines that could not be read
}

// writePing sends one ping, so the daemon keeps a quiet connection alive.
func writePing(w io.Writer, conv *Converter, now time.Time) error {
	b, err := json.Marshal(conv.Ping(now.Unix()))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// maxPause caps the wait between two events when replaying in real time, so an
// idle stretch in a capture does not stall the replay.
const maxPause = time.Second

// Replay reads a prototype capture from in, converts it, and writes the
// messages to out, one JSON line each. speed 0 sends as fast as it can; 1
// follows the capture's own timing (each pause capped at maxPause); 10 is ten
// times faster. sleep is time.Sleep, or a recorder in tests.
func Replay(ctx context.Context, conv *Converter, in io.Reader, out io.Writer, speed float64, sleep func(time.Duration)) (Stats, error) {
	var st Stats
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var prev int64
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		st.Lines++
		msgs, err := conv.Convert(sc.Bytes())
		if err != nil {
			st.Unusable++
			continue
		}
		if len(msgs) == 0 {
			st.Skipped++
			continue
		}
		for _, m := range msgs {
			if speed > 0 && prev != 0 && m.TSSec >= prev {
				if d := time.Duration(float64(time.Duration(m.TSSec-prev)*time.Second) / speed); d > 0 {
					if d > maxPause {
						d = maxPause
					}
					sleep(d)
				}
			}
			if m.TSSec != 0 {
				prev = m.TSSec
			}
			b, err := json.Marshal(m)
			if err != nil {
				return st, err
			}
			if _, err := out.Write(append(b, '\n')); err != nil {
				return st, err
			}
			st.Sent++
		}
	}
	return st, sc.Err()
}
