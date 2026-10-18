package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:7357": true,
		"localhost:7357": true,
		"[::1]:7357":     true,
		"0.0.0.0:7357":   false,
		":7357":          false,
		"10.1.2.3:80":    false,
		"nonsense":       false,
	} {
		if err := checkLoopback(addr); (err == nil) != ok {
			t.Errorf("checkLoopback(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}

func TestServeRefusesPublicAddr(t *testing.T) {
	_, err := run(t, "serve", "--addr", "0.0.0.0:7357", "--socket", "/nonexistent.sock")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %v", err)
	}
}

func TestServeShowsDownPage(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	sock := filepath.Join(t.TempDir(), "none.sock")
	go func() { done <- runServe(ctx, cmd, sock, "127.0.0.1:0", ready) }()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("runServe exited: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "sentientd is not reachable") {
		t.Errorf("GET / = %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(out.String(), "sen serve: http://127.0.0.1:") {
		t.Errorf("startup line = %q", out.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestServeAddrInUse(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cmd, "/nonexistent.sock", "127.0.0.1:0", ready) }()
	addr := <-ready
	if err := runServe(context.Background(), cmd, "/nonexistent.sock", addr, nil); err == nil {
		t.Error("want error when the address is taken")
	}
	cancel()
	<-done
}
