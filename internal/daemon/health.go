package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/store"
	"github.com/nosaka4i/sentient/internal/version"
)

// monitor builds sentientd's health report.
type monitor struct {
	st      *store.Store
	dataDir string
	socket  string
	homes   []string
	started time.Time
	now     func() time.Time
	log     *slog.Logger

	cpu   *cpuSampler
	integ *integrity
	sudo  *sudoChecker
}

func newMonitor(st *store.Store, cfg Config, log *slog.Logger) *monitor {
	now := time.Now
	return &monitor{
		st: st, dataDir: cfg.DataDir, socket: cfg.Socket, homes: cfg.Homes,
		started: now(), now: now, log: log,
		cpu:   newCPUSampler(now),
		integ: &integrity{st: st, now: now, interval: cfg.IntegrityInterval, log: log},
		sudo:  &sudoChecker{now: now, run: runSudoList, root: os.Geteuid() == 0, users: agentAccounts(cfg.Homes)},
	}
}

// run samples CPU and runs the periodic full integrity check.
func (m *monitor) run(ctx context.Context) {
	go m.integ.loop(ctx)
	t := time.NewTicker(cpuSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.cpu.sample()
		}
	}
}

// report implements api.HealthFunc.
func (m *monitor) report(ctx context.Context) (*health.Report, error) {
	now := m.now()
	r := &health.Report{CheckedAt: now}
	var err error
	if r.HistoryBegins, err = query.HistoryBegins(ctx, m.st.DB); err != nil {
		return nil, err
	}

	u := "unknown"
	if cu, err := user.Current(); err == nil {
		u = cu.Username
	}
	cpu, window := m.cpu.percent()
	r.Daemon = health.Daemon{
		Version: version.Version, Started: m.started, CPUPercent: cpu, CPUWindow: window,
		RSSBytes: rssBytes(), Socket: m.socket, User: u, Root: os.Geteuid() == 0,
	}
	r.Storage = storage(m.dataDir)

	if r.Ingest, err = query.IngestStats(ctx, m.st.DB); err != nil {
		return nil, err
	}
	if r.Agents, err = query.Agents(ctx, m.st.DB); err != nil {
		return nil, err
	}
	if r.Live, err = query.LiveSessions(ctx, m.st.DB, now.Add(-query.RunningWindow)); err != nil {
		return nil, err
	}
	m.integ.incremental(ctx)
	r.Integrity = m.integ.state()
	r.Sudo = m.sudo.check(ctx)
	return r, nil
}

// storage reports the database's size and the free space beside it.
func storage(dataDir string) health.Storage {
	s := health.Storage{DataDir: dataDir, DBPath: filepath.Join(dataDir, store.DBFileName)}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(s.DBPath + suffix); err == nil {
			s.DBBytes += fi.Size()
		}
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dataDir, &fs); err == nil {
		s.FreeBytes = int64(fs.Bavail) * int64(fs.Bsize)
		s.SizeBytes = int64(fs.Blocks) * int64(fs.Bsize)
	}
	return s
}

// rssBytes is the process's resident memory (Linux /proc; else peak RSS).
func rssBytes() int64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		if n, ok := parseVmRSS(b); ok {
			return n
		}
	}
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err == nil {
		return maxRSSBytes(runtime.GOOS, int64(ru.Maxrss))
	}
	return 0
}

// maxRSSBytes converts getrusage's peak RSS to bytes: Linux reports
// kilobytes, macOS bytes.
func maxRSSBytes(goos string, v int64) int64 {
	if goos == "darwin" {
		return v
	}
	return v * 1024
}

func parseVmRSS(status []byte) (int64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "VmRSS:" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}

// cpuSampleEvery and cpuSamples give a 5-minute CPU average.
const (
	cpuSampleEvery = 30 * time.Second
	cpuSamples     = 10
)

// cpuSampler keeps recent (wall, CPU time) samples of this process.
type cpuSampler struct {
	mu      sync.Mutex
	now     func() time.Time
	cpuTime func() time.Duration
	samples []cpuSample
}

type cpuSample struct {
	wall time.Time
	cpu  time.Duration
}

func newCPUSampler(now func() time.Time) *cpuSampler {
	c := &cpuSampler{now: now, cpuTime: processCPUTime}
	c.sample()
	return c
}

func (c *cpuSampler) sample() {
	s := cpuSample{wall: c.now(), cpu: c.cpuTime()}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, s)
	if len(c.samples) > cpuSamples+1 {
		c.samples = c.samples[len(c.samples)-cpuSamples-1:]
	}
}

// percent is CPU use since the oldest kept sample, as a percentage of one
// core, and the window it covers.
func (c *cpuSampler) percent() (float64, time.Duration) {
	cur := cpuSample{wall: c.now(), cpu: c.cpuTime()}
	c.mu.Lock()
	oldest := c.samples[0]
	c.mu.Unlock()
	wall := cur.wall.Sub(oldest.wall)
	if wall <= 0 {
		return 0, 0
	}
	return 100 * float64(cur.cpu-oldest.cpu) / float64(wall), wall
}

func processCPUTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// incrementalEvery throttles the on-demand (incremental) chain check.
const incrementalEvery = 10 * time.Second

// integrity runs the hash-chain check: a full pass on start and every
// interval, and a cheap incremental pass (only records appended since the
// last check) when the health page asks.
type integrity struct {
	st       *store.Store
	now      func() time.Time
	interval time.Duration
	log      *slog.Logger

	checking sync.Mutex // one check at a time

	mu        sync.Mutex
	point     store.ChainPoint
	checkedAt time.Time
	fullAt    time.Time
	records   int64
	bad       int64
}

func (i *integrity) loop(ctx context.Context) {
	for {
		i.full(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(i.interval):
		}
	}
}

func (i *integrity) full(ctx context.Context) {
	i.checking.Lock()
	defer i.checking.Unlock()
	res, err := i.st.VerifyChainFrom(ctx, store.ChainPoint{})
	if err != nil {
		if ctx.Err() == nil {
			i.log.Error("integrity check failed to run", "err", err)
		}
		return
	}
	now := i.now()
	i.mu.Lock()
	defer i.mu.Unlock()
	i.point, i.records, i.bad = res.Last, res.Checked, res.BadID
	i.checkedAt, i.fullAt = now, now
	if res.BadID != 0 {
		i.log.Error("HASH CHAIN BROKEN: a raw log record was changed or removed", "record_id", res.BadID)
	}
}

// incremental checks records appended since the last check. It's skipped
// while another check runs, before the first full check, after a
// failure (that stands until the next full check), and when one ran
// within incrementalEvery.
func (i *integrity) incremental(ctx context.Context) {
	if !i.checking.TryLock() {
		return
	}
	defer i.checking.Unlock()
	i.mu.Lock()
	skip := i.fullAt.IsZero() || i.bad != 0 || i.now().Sub(i.checkedAt) < incrementalEvery
	from := i.point
	i.mu.Unlock()
	if skip {
		return
	}
	res, err := i.st.VerifyChainFrom(ctx, from)
	if err != nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.point, i.bad = res.Last, res.BadID
	i.records += res.Checked
	i.checkedAt = i.now()
}

func (i *integrity) state() health.Integrity {
	i.mu.Lock()
	defer i.mu.Unlock()
	return health.Integrity{
		CheckedAt: i.checkedAt, FullAt: i.fullAt, Records: i.records,
		OK: i.bad == 0, BadRecordID: i.bad, Interval: i.interval,
	}
}

// sudoCacheFor is how long a sudo check result is reused.
const sudoCacheFor = 10 * time.Minute

// sudoChecker checks whether the agents' accounts have passwordless sudo.
// Only root can ask sudo about another user without a password prompt.
type sudoChecker struct {
	now   func() time.Time
	run   func(ctx context.Context, user string) (string, error)
	root  bool
	users []string

	mu     sync.Mutex
	at     time.Time
	result health.Sudo
}

func (s *sudoChecker) check(ctx context.Context) health.Sudo {
	if !s.root {
		return health.Sudo{Reason: "unknown (sentientd not running as root)"}
	}
	if len(s.users) == 0 {
		return health.Sudo{Reason: "no agent accounts found"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && s.now().Sub(s.at) < sudoCacheFor {
		return s.result
	}
	res := health.Sudo{Checked: true}
	var failed []string
	for _, u := range s.users {
		out, err := s.run(ctx, u)
		if err != nil && out == "" {
			failed = append(failed, u)
			continue
		}
		if strings.Contains(out, "NOPASSWD") {
			res.Passwordless = append(res.Passwordless, u)
		}
	}
	if len(failed) == len(s.users) {
		res = health.Sudo{Reason: "unknown (sudo -l failed)"}
	}
	s.at, s.result = s.now(), res
	return res
}

// runSudoList asks sudo what user may run, without prompting.
func runSudoList(ctx context.Context, u string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", "-l", "-U", u).CombinedOutput()
	return string(out), err
}

// agentAccounts returns the owners of the homes that have Claude Code
// logs: the accounts agents run as.
func agentAccounts(homes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range homes {
		fi, err := os.Stat(filepath.Join(h, ".claude", "projects"))
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		name := fmt.Sprint(st.Uid)
		if u, err := user.LookupId(name); err == nil {
			name = u.Username
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
