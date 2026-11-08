package tetragon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nosaka4i/sentient/internal/collector"
)

// DefaultAddr is where Tetragon serves gRPC in a standard install (its
// systemd unit and packaged config): a root-owned Unix socket.
const DefaultAddr = "unix:///var/run/tetragon/tetragon.sock"

// Collector reads Tetragon's event stream and reports it to a Sink. While
// Tetragon is unreachable or the stream is down it records a monitor gap, and
// it keeps retrying.
type Collector struct {
	Addr     string        // gRPC target; DefaultAddr when empty
	Agents   []string      // agent binary prefixes, see policy.AgentPrefixes
	Retry    time.Duration // wait between attempts; 2s when zero
	ConnWait time.Duration // how long to wait for a connection per attempt; 3s when zero
	Now      func() time.Time
	Status   *collector.Status // updated as the connection changes; nil to not track
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return Name }

// NewStatus returns the status a Collector for addr reports to.
func NewStatus(addr string, now time.Time) *collector.Status {
	if addr == "" {
		addr = DefaultAddr
	}
	return collector.NewStatus("Tetragon", "eBPF on Linux", "socket", addr, now)
}

func (c *Collector) addr() string {
	if c.Addr == "" {
		return DefaultAddr
	}
	return c.Addr
}

func (c *Collector) retry() time.Duration {
	if c.Retry == 0 {
		return 2 * time.Second
	}
	return c.Retry
}

func (c *Collector) connWait() time.Duration {
	if c.ConnWait == 0 {
		return 3 * time.Second
	}
	return c.ConnWait
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Run implements collector.Collector. It returns when ctx is done, or when the
// sink fails (a store write error is not something to retry around).
func (c *Collector) Run(ctx context.Context, sink collector.Sink) error {
	conn, err := grpc.NewClient(c.addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("tetragon: %w", err)
	}
	defer conn.Close()
	client := tetragon.NewFineGuidanceSensorsClient(conn)
	norm := NewNormalizer(sink, c.Agents)

	var gap int64 // open gap id, 0 when none
	openGap := func(reason string) error {
		if gap != 0 {
			return nil
		}
		id, err := sink.GapOpened(ctx, c.now(), reason)
		gap = id
		return err
	}
	for ctx.Err() == nil {
		if !waitReady(ctx, conn, c.connWait()) {
			if ctx.Err() != nil {
				break
			}
			c.Status.SetState(collector.Reconnecting, time.Now(), "connect to "+c.addr()+": "+dialError(c.addr()))
			if err := openGap("tetragon unreachable at " + c.addr()); err != nil {
				return err
			}
			sleep(ctx, c.retry())
			continue
		}
		if gap != 0 {
			if err := sink.GapClosed(ctx, gap, c.now()); err != nil {
				return err
			}
			gap = 0
		}
		c.Status.SetState(collector.Connected, time.Now(), "")
		c.Status.SetVersion(c.version(ctx, client))
		err := c.stream(ctx, client, norm)
		if ctx.Err() != nil {
			break
		}
		if sinkErr, ok := err.(sinkError); ok {
			return sinkErr.error
		}
		c.Status.SetState(collector.Reconnecting, time.Now(), fmt.Sprintf("stream ended: %v", err))
		if err := openGap(fmt.Sprintf("tetragon stream ended: %v", err)); err != nil {
			return err
		}
		sleep(ctx, c.retry())
	}
	return nil
}

// sinkError marks an error that came from the sink, not from the stream.
type sinkError struct{ error }

func (c *Collector) stream(ctx context.Context, client tetragon.FineGuidanceSensorsClient, norm *Normalizer) error {
	stream, err := client.GetEvents(ctx, &tetragon.GetEventsRequest{
		AllowList: []*tetragon.Filter{{EventSet: []tetragon.EventType{
			tetragon.EventType_PROCESS_EXEC, tetragon.EventType_PROCESS_EXIT, tetragon.EventType_PROCESS_KPROBE,
		}}},
	})
	if err != nil {
		return err
	}
	for {
		r, err := stream.Recv()
		if err != nil {
			return err
		}
		c.Status.CountEvent(time.Now())
		if err := norm.Handle(ctx, r); err != nil {
			return sinkError{err}
		}
	}
}

// version asks Tetragon for its version; empty when it does not say.
func (c *Collector) version(ctx context.Context, client tetragon.FineGuidanceSensorsClient) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := client.GetVersion(ctx, &tetragon.GetVersionRequest{})
	if err != nil {
		return ""
	}
	return r.GetVersion()
}

// dialError says why addr cannot be reached ("permission denied", "no such
// file or directory", ...), by trying a plain connection.
func dialError(addr string) string {
	network, target := "tcp", addr
	if p, ok := strings.CutPrefix(addr, "unix://"); ok {
		network, target = "unix", p
	} else if p, ok := strings.CutPrefix(addr, "unix:"); ok {
		network, target = "unix", p
	}
	conn, err := net.DialTimeout(network, target, time.Second)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Err != nil {
			return op.Err.Error()
		}
		return err.Error()
	}
	conn.Close()
	return "not answering"
}

// waitReady connects and waits up to d for the connection to be ready.
func waitReady(ctx context.Context, conn *grpc.ClientConn, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	conn.Connect()
	for {
		s := conn.GetState()
		if s == connectivity.Ready {
			return true
		}
		if s == connectivity.TransientFailure {
			return false // refused or no such socket: don't wait out the timeout
		}
		if !conn.WaitForStateChange(ctx, s) {
			return false
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
