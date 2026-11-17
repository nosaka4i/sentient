package es

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nosaka4i/sentient/internal/collector"
)

// DefaultSocket is where sentientd listens for the extension on the Mac: a
// root-only directory, since the extension runs as root.
const DefaultSocket = "/var/run/sentient/es.sock"

// Config is what the extension is told at connect, in the reply to its hello:
// the agent executable prefixes, the path prefixes it mutes in the kernel, and
// the sensitive paths it must never mute. The extension applies it live, so a
// change needs a sentientd restart, not a new extension.
type Config struct {
	Agents    []string // executable path prefixes that make a process an agent
	Noise     []string // path prefixes whose opens are muted in the kernel
	Sensitive []string // path prefixes that are never muted
}

// configMsg is the "config" line sent to the extension.
type configMsg struct {
	V         int      `json:"v"`
	T         string   `json:"t"`
	Agents    []string `json:"agents"`
	Noise     []string `json:"noise"`
	Sensitive []string `json:"sensitive"`
}

// Collector listens on a Unix socket for the Endpoint Security extension and
// reports what it sends to a Sink. While the extension is not connected, has
// failed to start, has stopped answering or has dropped events, it records a
// monitor gap.
type Collector struct {
	Socket      string        // Unix socket to listen on; DefaultSocket when empty
	Config      Config        // sent to the extension at connect
	PingTimeout time.Duration // silence after which a connected extension is taken as hung; 30s when zero
	Now         func() time.Time
	Status      *collector.Status // updated as the connection changes; nil to not track
	Log         *slog.Logger      // nil to stay quiet

	mu      sync.Mutex
	dropped map[string]int64 // the extension's own drop counts, from stats messages
	bad     int64            // lines that could not be decoded
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return Name }

// NewStatus returns the status a Collector for socket reports to.
func NewStatus(socket string, now time.Time) *collector.Status {
	if socket == "" {
		socket = DefaultSocket
	}
	return collector.NewStatus("Endpoint Security", "Mac", "socket", socket, now)
}

func (c *Collector) socket() string {
	if c.Socket == "" {
		return DefaultSocket
	}
	return c.Socket
}

func (c *Collector) pingTimeout() time.Duration {
	if c.PingTimeout == 0 {
		return 30 * time.Second
	}
	return c.PingTimeout
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) logf(msg string, args ...any) {
	if c.Log != nil {
		c.Log.Info(msg, args...)
	}
}

// Dropped is how many events the extension reported dropping, by reason, and
// how many lines could not be decoded, since the collector was made.
func (c *Collector) Dropped() (byReason map[string]int64, undecodable int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.dropped))
	for k, v := range c.dropped {
		out[k] = v
	}
	return out, c.bad
}

// Run implements collector.Collector. It listens until ctx is done and serves
// one extension connection at a time; a new connection is accepted after the
// last one ends. It returns early only when the socket cannot be opened or the
// sink fails (a store write error is not something to retry around).
func (c *Collector) Run(ctx context.Context, sink collector.Sink) error {
	ln, err := listen(c.socket())
	if err != nil {
		return fmt.Errorf("endpointsecurity: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	defer ln.Close()

	s := &session{c: c, sink: sink, norm: NewNormalizer(sink)}
	if err := s.openGap(ctx, "endpoint security extension has not connected"); err != nil {
		return err
	}
	c.Status.SetState(collector.Connecting, c.now(), "")
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("endpointsecurity: accept: %w", err)
		}
		err = s.serve(ctx, conn)
		conn.Close()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			var se sinkError
			if errors.As(err, &se) {
				return se.error
			}
		}
	}
}

// listen opens the Unix socket, replacing a stale one and keeping it
// root-only (0600).
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// startFailure begins the gap reason of an extension whose Endpoint Security
// client did not start.
const startFailure = "endpoint security client did not start"

// sinkError marks an error that came from the sink, not from the connection.
type sinkError struct{ error }

// session is the state of the collector across connections: the open gap and
// the normalizer.
type session struct {
	c    *Collector
	sink collector.Sink
	norm *Normalizer
	gap  int64 // id of the open gap, 0 when none

	gapReason string // why the open gap was opened
}

func (s *session) openGap(ctx context.Context, reason string) error {
	if s.gap != 0 {
		return nil
	}
	id, err := s.sink.GapOpened(ctx, s.c.now(), reason)
	if err != nil {
		return sinkError{err}
	}
	s.gap, s.gapReason = id, reason
	return nil
}

func (s *session) closeGap(ctx context.Context) error {
	if s.gap == 0 {
		return nil
	}
	err := s.sink.GapClosed(ctx, s.gap, s.c.now())
	s.gap = 0
	if err != nil {
		return sinkError{err}
	}
	return nil
}

// span records a gap that is already over: the time between two messages in
// which events were lost.
func (s *session) span(ctx context.Context, from, to time.Time, reason string) error {
	id, err := s.sink.GapOpened(ctx, from, reason)
	if err != nil {
		return sinkError{err}
	}
	if err := s.sink.GapClosed(ctx, id, to); err != nil {
		return sinkError{err}
	}
	return nil
}

// serve reads one connection until it ends, and returns why: a sinkError when
// the store failed, otherwise nil (the connection ended and a gap is open).
func (s *session) serve(ctx context.Context, conn net.Conn) error {
	c := s.c
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	s.norm.Reset() // the extension may have restarted: its tree is new
	r := bufio.NewReaderSize(conn, 64<<10)

	var (
		started     bool   // a valid hello came in
		lastSeq     uint64 // the previous message's sequence number
		lastTime    = c.now()
		lastDropped int64
	)
	for {
		conn.SetReadDeadline(c.now().Add(c.pingTimeout()))
		line, err := readLine(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			reason := "endpoint security extension disconnected"
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				reason = fmt.Sprintf("endpoint security extension stopped responding (no message for %s)", c.pingTimeout())
			}
			if s.gap != 0 && strings.HasPrefix(s.gapReason, startFailure) {
				// the extension exits after a failed start; Health keeps saying why
				reason = s.gapReason
			}
			c.Status.SetState(collector.Reconnecting, c.now(), reason)
			return s.openGap(ctx, reason)
		}
		m, err := Decode(line)
		if err != nil {
			c.mu.Lock()
			c.bad++
			c.mu.Unlock()
			c.logf("endpointsecurity: dropped an undecodable line", "err", err)
			continue
		}
		at := time.Unix(m.TSSec, m.TSNsec)
		if m.TSSec == 0 {
			at = c.now()
		}
		// a jump in the extension's own numbering means messages were lost
		// between the last one and this
		if started && m.T != KindHello && m.Seq != 0 && lastSeq != 0 && m.Seq != lastSeq+1 {
			if err := s.span(ctx, lastTime, at, fmt.Sprintf("endpoint security extension lost %d messages", m.Seq-lastSeq-1)); err != nil {
				return err
			}
		}
		if m.Seq != 0 {
			lastSeq = m.Seq
		}
		prevTime := lastTime
		lastTime = at

		switch m.T {
		case KindHello:
			started = true
			lastSeq = m.Seq
			if err := s.hello(ctx, conn, m); err != nil {
				return err
			}
		case KindStats:
			var total int64
			for _, n := range m.Dropped {
				total += n
			}
			c.mu.Lock()
			if c.dropped == nil {
				c.dropped = map[string]int64{}
			}
			for k, n := range m.Dropped {
				c.dropped[k] = n
			}
			c.mu.Unlock()
			if total > lastDropped {
				if err := s.span(ctx, prevTime, at, fmt.Sprintf("endpoint security extension dropped %d events (%s)", total-lastDropped, dropReasons(m.Dropped))); err != nil {
					return err
				}
			}
			lastDropped = total
		case KindPing:
			// only proves the extension is alive; the read deadline was renewed above
		default:
			if !started {
				continue // events before a hello: not a protocol we know
			}
			c.Status.CountEvent(c.now())
			if err := s.norm.Handle(ctx, m); err != nil {
				return sinkError{err}
			}
		}
	}
}

// hello handles the extension's first message: it answers with the config, and
// closes the gap if the extension's Endpoint Security client started. When the
// client did not start (Full Disk Access missing, or not approved), the gap
// stays open with the reason, so Health says so instead of staying silent.
func (s *session) hello(ctx context.Context, conn net.Conn, m Message) error {
	c := s.c
	if m.Ext != "" {
		c.Status.SetVersion(helloVersion(m))
	}
	if err := writeConfig(conn, c.Config); err != nil {
		c.logf("endpointsecurity: could not send the config", "err", err)
	}
	if m.ESClient != "" && m.ESClient != "ok" {
		reason := startFailure + ": " + m.ESClient
		if m.ESClient == "not_permitted" {
			reason += " (Full Disk Access is missing, or the extension is not approved)"
		}
		c.Status.SetState(collector.Reconnecting, c.now(), reason)
		if s.gap != 0 && s.gapReason == reason {
			return nil // the same failure again: one gap, not one per retry
		}
		// replace "has not connected" (or an older reason) with the real one
		if err := s.closeGap(ctx); err != nil {
			return err
		}
		return s.openGap(ctx, reason)
	}
	c.Status.SetState(collector.Connected, c.now(), "")
	return s.closeGap(ctx)
}

func helloVersion(m Message) string {
	if m.Build != "" {
		return m.Ext + " (build " + m.Build + ")"
	}
	return m.Ext
}

func writeConfig(w io.Writer, cfg Config) error {
	b, err := json.Marshal(configMsg{V: SchemaVersion, T: "config",
		Agents: orEmpty(cfg.Agents), Noise: orEmpty(cfg.Noise), Sensitive: orEmpty(cfg.Sensitive)})
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func dropReasons(d map[string]int64) string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s %d", k, d[k])
	}
	return out
}

// maxLine bounds one message. A command line can be long (a shell snapshot
// wrapper is several KB); anything larger is not a message we want.
const maxLine = 4 << 20

// readLine reads one line, without its newline. A line longer than maxLine is
// an error that ends the connection.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, part...)
		if len(line) > maxLine {
			return nil, errors.New("line too long")
		}
		if !isPrefix {
			return line, nil
		}
	}
}
