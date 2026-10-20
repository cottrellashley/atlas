//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/harness"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const ProtocolVersion = 1

func Socket(home string) string { return filepath.Join(home, "daemon.sock") }
func Serve(ctx context.Context, home string, f Factory) error {
	if e := os.MkdirAll(home, 0700); e != nil {
		return e
	}
	info, e := os.Lstat(home)
	if e != nil {
		return e
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("ATLAS_HOME must be a private directory (chmod 700), not a symlink")
	}
	lock, e := os.OpenFile(filepath.Join(home, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("an Atlas daemon is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	m, e := NewManager(home, f)
	if e != nil {
		return e
	}
	socket := Socket(home)
	if e = os.Remove(socket); e != nil && !os.IsNotExist(e) {
		return e
	}
	listener, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer listener.Close()
	defer os.Remove(socket)
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	shutdown := make(chan struct{}, 1)
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, e error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if e := d.Decode(v); e != nil {
			fail(w, e)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"protocol": ProtocolVersion, "pid": os.Getpid()})
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) { reply(w, m.List()) })
	mux.HandleFunc("GET /session", func(w http.ResponseWriter, r *http.Request) {
		v, e := m.Get(r.URL.Query().Get("id"))
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, v)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		after, e := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if e != nil {
			fail(w, e)
			return
		}
		v, e := m.Events(r.URL.Query().Get("id"), after)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, v)
	})
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var in RunInput
		if !decode(w, r, &in) {
			return
		}
		v, e := m.Run(in)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, v)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, e := m.Send(in.ID, in.Prompt)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, v)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if e := m.Stop(ctx, in.ID); e != nil {
			fail(w, e)
			return
		}
		reply(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("POST /permission", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID      string          `json:"id"`
			Control harness.Control `json:"control"`
		}
		if !decode(w, r, &in) {
			return
		}
		if e := m.Permission(in.ID, in.Control); e != nil {
			fail(w, e)
			return
		}
		reply(w, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /configure", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID    string             `json:"id"`
			Patch map[string]*string `json:"patch"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, e := m.Configure(in.ID, in.Patch)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, v)
	})
	mux.HandleFunc("POST /archive", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID       string `json:"id"`
			Archived bool   `json:"archived"`
		}
		if !decode(w, r, &in) {
			return
		}
		if e := m.Archive(in.ID, in.Archived); e != nil {
			fail(w, e)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /import", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Harness string `json:"harness"`
			CWD     string `json:"cwd"`
			Handle  string `json:"handle"`
		}
		if !decode(w, r, &in) {
			return
		}
		c, e := config.Load(home)
		if e != nil {
			fail(w, e)
			return
		}
		cfg, e := c.Resolve(in.Harness)
		if e != nil {
			fail(w, e)
			return
		}
		driver, e := f(cfg)
		if e != nil {
			fail(w, e)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		items, e := driver.Discover(ctx, in.CWD, 5000)
		if e != nil {
			fail(w, e)
			return
		}
		for _, n := range items {
			var data map[string]any
			_ = json.Unmarshal(n.Handle.Data, &data)
			match := n.ID == in.Handle || string(n.Handle.Data) == in.Handle
			for _, key := range []string{"sessionFile", "file", "path", "nativeHandle", "sessionId"} {
				if v, ok := data[key].(string); ok && v == in.Handle {
					match = true
				}
			}
			if match {
				v, e := m.Import(in.Harness, n)
				if e != nil {
					fail(w, e)
					return
				}
				reply(w, v)
				return
			}
		}
		fail(w, fmt.Errorf("native session %q not found in discovery for this directory", in.Handle))
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"stopping": true})
		select {
		case shutdown <- struct{}{}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
	case <-shutdown:
	case e = <-errCh:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	stopErr := m.Shutdown(closeCtx)
	httpErr := server.Shutdown(closeCtx)
	if stopErr != nil {
		return stopErr
	}
	return httpErr
}
