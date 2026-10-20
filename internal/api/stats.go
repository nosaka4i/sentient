package api

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	"github.com/nosaka4i/sentient/internal/health"
)

// ClientHeader is the metadata key clients use to name themselves
// ("sen status", "sen serve"), for the health page's "clients now".
const ClientHeader = "sentient-client"

// latencySamples is how many recent request latencies the percentiles
// are computed over.
const latencySamples = 1024

// Stats records local API traffic since sentientd started: requests and
// errors per method, recent latencies, the request rate over the last
// minute, and connected clients. Small and in memory; resets on restart.
type Stats struct {
	mu      sync.Mutex
	now     func() time.Time
	started time.Time
	methods map[string]int64
	errors  map[string]int64 // by status code name
	lat     [latencySamples]time.Duration
	latN    int // samples recorded (ring position = latN % len)
	secs    [60]secCount
	conns   map[uint64]string // live connection -> client name ("" until it names itself)
	nextID  uint64
}

type secCount struct {
	sec int64
	n   int64
}

// NewStats returns empty stats starting now.
func NewStats() *Stats {
	s := &Stats{now: time.Now, methods: map[string]int64{}, errors: map[string]int64{}, conns: map[uint64]string{}}
	s.started = s.now()
	return s
}

// UnaryInterceptor counts each request, its outcome and latency.
func (s *Stats) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := s.now()
		resp, err := handler(ctx, req)
		s.record(path.Base(info.FullMethod), s.now().Sub(start), err)
		return resp, err
	}
}

func (s *Stats) record(method string, d time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method]++
	if err != nil {
		s.errors[status.Code(err).String()]++
	}
	s.lat[s.latN%latencySamples] = d
	s.latN++
	sec := s.now().Unix()
	slot := &s.secs[sec%int64(len(s.secs))]
	if slot.sec != sec {
		*slot = secCount{sec: sec}
	}
	slot.n++
}

type connKey struct{}

// StatsHandler tracks connections and the client name each one sends.
func (s *Stats) StatsHandler() stats.Handler { return (*connHandler)(s) }

type connHandler Stats

func (h *connHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	s := (*Stats)(h)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.conns[id] = ""
	s.mu.Unlock()
	return context.WithValue(ctx, connKey{}, id)
}

func (h *connHandler) HandleConn(ctx context.Context, cs stats.ConnStats) {
	if _, ok := cs.(*stats.ConnEnd); !ok {
		return
	}
	if id, ok := ctx.Value(connKey{}).(uint64); ok {
		s := (*Stats)(h)
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
	}
}

func (h *connHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

func (h *connHandler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	in, ok := rs.(*stats.InHeader)
	if !ok {
		return
	}
	id, ok := ctx.Value(connKey{}).(uint64)
	if !ok {
		return
	}
	name := "unknown"
	if v := in.Header.Get(ClientHeader); len(v) > 0 && v[0] != "" {
		name = v[0]
	}
	s := (*Stats)(h)
	s.mu.Lock()
	if _, live := s.conns[id]; live {
		s.conns[id] = name
	}
	s.mu.Unlock()
}

// Snapshot returns the stats for the health report.
func (s *Stats) Snapshot() health.API {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	a := health.API{Since: s.started}
	for m, n := range s.methods {
		a.Requests += n
		a.Methods = append(a.Methods, health.NamedCount{Name: m, Count: n})
	}
	for c, n := range s.errors {
		a.Errors += n
		a.ErrorsByCode = append(a.ErrorsByCode, health.NamedCount{Name: c, Count: n})
	}
	byCount(a.Methods)
	byCount(a.ErrorsByCode)

	var lastMin int64
	for _, c := range s.secs {
		if age := now.Unix() - c.sec; age >= 0 && age < int64(len(s.secs)) {
			lastMin += c.n
		}
	}
	window := float64(len(s.secs))
	if up := now.Sub(s.started).Seconds(); up < window {
		window = max(up, 1)
	}
	a.PerSecNow = float64(lastMin) / window

	n := min(s.latN, latencySamples)
	if n > 0 {
		lat := append([]time.Duration(nil), s.lat[:n]...)
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		pct := func(p int) time.Duration { return lat[(n-1)*p/100] }
		a.P50, a.P95, a.P99, a.LatencyCount = pct(50), pct(95), pct(99), n
	}

	for _, name := range s.conns {
		if name != "" {
			a.Clients = append(a.Clients, name)
		}
	}
	sort.Strings(a.Clients)
	return a
}

func byCount(c []health.NamedCount) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Count != c[j].Count {
			return c[i].Count > c[j].Count
		}
		return c[i].Name < c[j].Name
	})
}

// clientInterceptor names the client on every call.
func clientInterceptor(name string) grpc.UnaryClientInterceptor {
	name = strings.TrimSpace(name)
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if name != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, ClientHeader, name)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
