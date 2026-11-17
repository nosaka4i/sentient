package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

func TestRunRefusesBothKernelCollectors(t *testing.T) {
	err := Run(context.Background(), Config{
		DataDir: t.TempDir(), Tetragon: "unix:///x", EndpointSecurity: "/y",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot both be on") {
		t.Fatalf("err = %v", err)
	}
}

func TestKernelNameAndMutes(t *testing.T) {
	if (Config{Tetragon: "x"}).kernelName() != "tetragon" || (Config{EndpointSecurity: "x"}).kernelName() != "endpointsecurity" {
		t.Error("collector names")
	}
	if (Config{}).kernelOn() || !(Config{EndpointSecurity: "x"}).kernelOn() || !(Config{Tetragon: "x"}).kernelOn() {
		t.Error("kernelOn")
	}
	c := &capture{level: policy.Essential, noise: []policy.NoisePath{
		{Path: "/System/", Category: "system files"},
		{Path: "/Library/", Category: "system files"},
		{Path: "/pkg/mod/", Category: "Go module cache", Anywhere: true},
	}}
	got := kernelMutes(c, []string{"/Library/Keychains/"})
	if len(got) != 1 || got[0] != "/System/" {
		t.Errorf("mutes at essential = %v", got)
	}
	c.level = policy.Complete
	if kernelMutes(c, nil) != nil {
		t.Error("nothing is muted at complete")
	}
	rep := captureReport(Config{EndpointSecurity: "/s", Verbosity: policy.Essential}, nil, nil)
	if !rep.Enabled || rep.PolicyFile != "" || rep.Level != "essential" {
		t.Errorf("report: %+v", rep)
	}
}

// TestRunWithEndpointSecurityStoresWhatTheExtensionSends runs sentientd with
// the Mac collector, plays the extension on the socket, and reads back what
// was stored: noise opens left out, sensitive ones kept, repeated writes
// counted once.
func TestRunWithEndpointSecurityStoresWhatTheExtensionSends(t *testing.T) {
	dir, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	esSock := filepath.Join(dir, "es.sock")
	home := filepath.Join(dir, "u")
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, Config{
			DataDir: data, Socket: filepath.Join(dir, "d.sock"), Homes: []string{home},
			EndpointSecurity: esSock, Verbosity: policy.Essential,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Ready: ready,
		})
	}()
	<-ready
	var conn net.Conn
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if conn, err = net.Dial("unix", esSock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the extension socket never came up")
		}
	}
	defer conn.Close()
	for _, l := range []string{
		`{"v":1,"t":"hello","seq":1,"ts_sec":1000,"ext":"1.0","build":"4","es_client":"ok"}`,
		`{"v":1,"t":"exec","seq":2,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"uid":501,"exe":"/bin/zsh","args":["zsh"]}`,
		// noise: the agent's own state, and a system file
		`{"v":1,"t":"file","seq":3,"ts_sec":1002,"pid":5,"pidver":1,"op":"open","path":"` + home + `/.claude/state.json","flags":1}`,
		`{"v":1,"t":"file","seq":4,"ts_sec":1002,"pid":5,"pidver":1,"op":"open","path":"/System/Library/x","flags":1}`,
		// sensitive: inside /Library, which is noise, and must be kept
		`{"v":1,"t":"file","seq":5,"ts_sec":1003,"pid":5,"pidver":1,"op":"open","path":"/Library/Keychains/System.keychain","flags":1}`,
		// three writes to one file
		`{"v":1,"t":"file","seq":6,"ts_sec":1004,"pid":5,"pidver":1,"op":"write","path":"` + home + `/proj/out.txt"}`,
		`{"v":1,"t":"file","seq":7,"ts_sec":1005,"pid":5,"pidver":1,"op":"write","path":"` + home + `/proj/out.txt"}`,
		`{"v":1,"t":"file","seq":8,"ts_sec":1006,"pid":5,"pidver":1,"op":"write","path":"` + home + `/proj/out.txt"}`,
		`{"v":1,"t":"exit","seq":9,"ts_sec":1007,"pid":5,"pidver":1,"status":0}`,
	} {
		if _, err := conn.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		st, err := store.Open(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		got = nil
		rows, err := st.DB.Query(`SELECT type || ' ' || COALESCE(path, '') FROM kernel_events ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			got = append(got, s)
		}
		rows.Close()
		var collectorName string
		var gapsOpen int
		st.DB.QueryRow(`SELECT COALESCE(MAX(collector), '') FROM processes`).Scan(&collectorName)
		st.DB.QueryRow(`SELECT COUNT(*) FROM monitor_gaps WHERE collector = 'endpointsecurity' AND ended_at IS NULL`).Scan(&gapsOpen)
		st.Close()
		if len(got) >= 5 {
			if collectorName != "endpointsecurity" || gapsOpen != 0 {
				t.Errorf("collector %q, %d open gaps", collectorName, gapsOpen)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored so far: %v", got)
		}
	}
	want := []string{
		"exec /bin/zsh",
		"file_open /Library/Keychains/System.keychain",
		"file_write " + home + "/proj/out.txt",
		"file_write " + home + "/proj/out.txt", // the count, reported when the process exits
		"exit ",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("stored:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
