package cli

import (
	"bytes"
	"context"
	"flag"
	"github.com/cottrellashley/atlas/internal/daemon"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInterspersedFlags(t *testing.T) {
	f := flag.NewFlagSet("x", flag.ContinueOnError)
	model := f.String("model", "", "m")
	bg := f.Bool("background", false, "b")
	if e := parse(f, []string{"hello", "--model", "vendor/model", "--background", "world"}); e != nil {
		t.Fatal(e)
	}
	if *model != "vendor/model" || !*bg || strings.Join(f.Args(), " ") != "hello world" {
		t.Fatal(f.Args())
	}
}

func TestForegroundCancelDuringStatusRequestStopsTurn(t *testing.T) {
	home, e := os.MkdirTemp("", "atlas-cancel-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(home)
	listener, e := net.Listen("unix", filepath.Join(home, "daemon.sock"))
	if e != nil {
		t.Fatal(e)
	}
	reached := make(chan struct{})
	var stopped atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) })
	mux.HandleFunc("GET /session", func(w http.ResponseWriter, r *http.Request) { close(reached); <-r.Context().Done() })
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		stopped.Store(true)
		_, _ = w.Write([]byte(`{"stopped":true}`))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	a := &App{Out: &out, Err: &out}
	done := make(chan error, 1)
	go func() { done <- a.follow(ctx, daemon.NewClient(home), "session", 0, false, 0, true) }()
	select {
	case <-reached:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("status request not reached")
	}
	select {
	case e := <-done:
		if e == nil || !stopped.Load() {
			t.Fatal("foreground cancel failed to stop", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground cancellation hung")
	}
}
func TestInitAndRoleSelection(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	a := &App{Home: t.TempDir(), Out: &out, Err: &out, In: strings.NewReader("")}
	if e := a.Run(context.Background(), []string{"init", "--harness", "pi", "--cwd", dir}); e != nil {
		t.Fatal(e)
	}
	cfg, p, _, e := a.selection("", dir)
	if e != nil || cfg.ID != "pi" || p.DefaultAgent != "researcher" {
		t.Fatal(cfg, p, e)
	}
}
func TestPromptStdin(t *testing.T) {
	a := &App{In: strings.NewReader("multiline\nprompt")}
	p, e := a.prompt([]string{"-"})
	if e != nil || p != "multiline\nprompt" {
		t.Fatal(p, e)
	}
}
