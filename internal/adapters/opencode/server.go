package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cottrellashley/atlas/internal/process"
)

type server struct {
	base, password, version string
	client                  *http.Client
	cancel                  context.CancelFunc
	exited                  chan struct{}
	stopOnce                sync.Once
	temp                    string
}

type responseError struct {
	status  int
	message string
}

func (e *responseError) Error() string { return e.message }

var listening = regexp.MustCompile(`server listening on (http://127\.0\.0\.1:[0-9]+)`)

func (d *driver) start(ctx context.Context) (*server, error) {
	return d.startIn(ctx, "")
}

// Resolve the configured executable in the caller's project before starting
// the native server in an isolated directory. Relative commands and PATH
// entries belong to the invocation, not the server's temporary workspace.
func (d *driver) startIn(ctx context.Context, cwd string) (*server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	executable, err := process.ResolveExecutable(d.config, cwd)
	if err != nil {
		return nil, fmt.Errorf("start OpenCode: %w", err)
	}
	dir, err := os.MkdirTemp("", "atlas-opencode-host-")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &server{password: hex.EncodeToString(key), client: &http.Client{Transport: &http.Transport{Proxy: nil}}, cancel: cancel, exited: make(chan struct{}), temp: dir}
	cfg := d.config
	cfg.Command = []string{executable}
	literal := false
	for _, arg := range d.config.Command[1:] {
		if arg == "--" {
			literal = true
		}
		// The interactive CLI accepts --standalone, but its explicit serve
		// command rejects it. An Atlas-owned private server already supplies
		// that behavior. Do not reinterpret arguments after a literal delimiter.
		if arg == "--standalone" && !literal {
			continue
		}
		cfg.Command = append(cfg.Command, arg)
	}
	cfg.Env = map[string]string{}
	for k, v := range d.config.Env {
		cfg.Env[k] = v
	}
	cfg.Env["OPENCODE_PASSWORD"] = s.password
	cmd := process.Command(lifetime, cfg, dir, "serve", "--hostname", "127.0.0.1", "--port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start OpenCode: %w", err)
	}
	urls := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 4096), 1024*1024)
		for scan.Scan() {
			if match := listening.FindStringSubmatch(scan.Text()); len(match) > 1 {
				select {
				case urls <- match[1]:
				default:
				}
			}
		}
	}()
	go func() { _ = cmd.Wait(); close(s.exited) }()
	startCtx, stop := context.WithTimeout(ctx, d.startup)
	defer stop()
	select {
	case s.base = <-urls:
	case <-s.exited:
		s.close()
		return nil, errors.New("OpenCode exited before announcing its local server URL")
	case <-startCtx.Done():
		s.close()
		return nil, fmt.Errorf("OpenCode server startup: %w", startCtx.Err())
	}
	var info struct {
		Version string `json:"version"`
	}
	if err = s.json(startCtx, "GET", "/api/info", nil, &info); err != nil {
		s.close()
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimPrefix(info.Version, "v"), "2.") {
		s.close()
		return nil, fmt.Errorf("OpenCode %s is unsupported by this adapter; install v2", info.Version)
	}
	s.version = info.Version
	return s, nil
}
func (s *server) close() {
	s.stopOnce.Do(func() { s.cancel(); <-s.exited; s.client.CloseIdleConnections(); _ = os.RemoveAll(s.temp) })
}
func (s *server) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return nil, err
	}
	r.SetBasicAuth("opencode", s.password)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	res, err := s.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("OpenCode %s %s: %w", method, path, err)
	}
	if res.StatusCode >= 300 || strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		var detail struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(raw, &detail)
		message := detail.Message
		if message == "" {
			message = detail.Error
		}
		if message == "" {
			message = http.StatusText(res.StatusCode)
		}
		return nil, &responseError{status: res.StatusCode, message: fmt.Sprintf("OpenCode %s %s returned HTTP %d: %s", method, path, res.StatusCode, message)}
	}
	return res, nil
}
func (s *server) json(ctx context.Context, method, path string, body, out any) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.request(bounded, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil || res.StatusCode == 204 {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode OpenCode %s: %w", path, err)
	}
	return nil
}
func (s *server) readyLocation(ctx context.Context, cwd string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		var plugins page[struct {
			ID    string `json:"id"`
			State struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"state"`
		}]
		if err := s.json(bounded, "GET", "/api/plugin"+locationQuery(cwd), nil, &plugins); err != nil {
			return err
		}
		if len(plugins.Data) > 0 {
			for _, p := range plugins.Data {
				if p.State.Status == "failed" {
					return fmt.Errorf("OpenCode plugin %s failed: %s", p.ID, p.State.Error)
				}
			}
			return nil
		}
		select {
		case <-bounded.Done():
			return fmt.Errorf("OpenCode workspace readiness: %w", bounded.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// MCP connections start asynchronously with the location. Let pending servers
// settle before the first prompt so their tools enter that prompt's catalog.
// Disabled/failed/auth-required servers retain native behavior; they do not
// prevent a user from working with the other available tools.
func (s *server) readyMCP(ctx context.Context, cwd string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		var servers page[struct {
			Status struct {
				Status string `json:"status"`
			} `json:"status"`
		}]
		if err := s.json(bounded, "GET", "/api/mcp"+locationQuery(cwd), nil, &servers); err != nil {
			return err
		}
		pending := false
		for _, server := range servers.Data {
			pending = pending || server.Status.Status == "pending"
		}
		if !pending {
			return nil
		}
		select {
		case <-bounded.Done():
			return fmt.Errorf("OpenCode MCP startup: %w", bounded.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type nativeEvent struct {
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	Durable struct {
		Seq int64 `json:"seq"`
	} `json:"durable"`
}

// SSE uses LF framing. Neither JSON strings containing U+2028 nor arbitrary
// UTF-8 byte boundaries split a protocol record.
func readSSE(reader io.Reader, visit func(nativeEvent) error) error {
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 4096), 16<<20)
	var fields []string
	eventName := ""
	dispatch := func() error {
		if len(fields) == 0 {
			return nil
		}
		raw := []byte(strings.Join(fields, "\n"))
		fields = nil
		if eventName == "effect/http-api/stream/failure" {
			return errors.New("OpenCode event stream reported failure")
		}
		// Some Effect encoders expose JSON-encoded strings; accept both
		// representations without changing the typed native event schema.
		if len(raw) > 0 && raw[0] == '"' {
			var decoded string
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return err
			}
			raw = []byte(decoded)
		}
		var e nativeEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("invalid OpenCode SSE event: %w", err)
		}
		return visit(e)
	}
	for scan.Scan() {
		line := strings.TrimSuffix(scan.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimPrefix(val, " ")
		switch key {
		case "data":
			fields = append(fields, val)
		case "event":
			eventName = val
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return dispatch()
}
func (s *server) events(ctx context.Context) (<-chan nativeEvent, <-chan error, func(), error) {
	streamCtx, cancel := context.WithCancel(ctx)
	res, err := s.request(streamCtx, "GET", "/api/event", nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	events := make(chan nativeEvent, 256)
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		defer close(failures)
		defer res.Body.Close()
		err := readSSE(res.Body, func(e nativeEvent) error {
			select {
			case events <- e:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
		if streamCtx.Err() == nil {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			failures <- err
		}
	}()
	return events, failures, func() { cancel(); _ = res.Body.Close(); <-done }, nil
}
func (s *server) log(ctx context.Context, id string, after int64) (int64, *nativeEvent, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := "/api/experimental/session/" + url.PathEscape(id) + "/log?follow=false&after=" + fmt.Sprint(after)
	res, err := s.request(bounded, "GET", path, nil)
	if err != nil {
		return after, nil, err
	}
	defer res.Body.Close()
	latest := after
	var terminal *nativeEvent
	err = readSSE(res.Body, func(e nativeEvent) error {
		if e.Durable.Seq <= after {
			return nil
		}
		if e.Durable.Seq > latest {
			latest = e.Durable.Seq
		}
		switch e.Type {
		case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
			copy := e
			terminal = &copy
		case "session.execution.started":
			terminal = nil
		}
		return nil
	})
	return latest, terminal, err
}
