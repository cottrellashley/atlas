package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
)

const maxFrameBytes = 32 << 20

// connection owns one process group. Its lifetime is deliberately independent
// of an individual read context so cancellation can first request interruption.
type connection struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	out       *io.PipeReader
	frames    chan frame
	done      chan struct{}
	cancel    context.CancelFunc
	waitErr   error // readable only after done closes
	stderr    tailBuffer
	writeMu   sync.Mutex
	closeOnce sync.Once
}
type frame struct {
	data json.RawMessage
	err  error
}
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = append([]byte(nil), b.data[len(b.data)-8192:]...)
	}
	return len(p), nil
}
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func start(ctx context.Context, cfg harness.Config, cwd string, args []string) (*connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &connection{cancel: cancel, frames: make(chan frame, 64), done: make(chan struct{})}
	c.cmd = process.Command(life, cfg, cwd, args...)
	var err error
	c.in, err = c.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	reader, writer := io.Pipe()
	c.out = reader
	c.cmd.Stdout = writer
	c.cmd.Stderr = &c.stderr
	if err = c.cmd.Start(); err != nil {
		cancel()
		c.in.Close()
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}
	go func() {
		defer close(c.frames)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), maxFrameBytes)
		for scanner.Scan() {
			line := append(json.RawMessage(nil), scanner.Bytes()...)
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			select {
			case c.frames <- frame{data: line}:
			case <-life.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			select {
			case c.frames <- frame{err: err}:
			case <-life.Done():
			}
		}
	}()
	go func() { c.waitErr = c.cmd.Wait(); close(c.done); writer.Close() }()
	return c, nil
}
func (c *connection) send(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.in.Write(b)
	if err != nil {
		return fmt.Errorf("write Claude control stream: %w", err)
	}
	return nil
}
func (c *connection) request(id, subtype string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["subtype"] = subtype
	return c.send(map[string]any{"type": "control_request", "request_id": id, "request": fields})
}
func (c *connection) next(ctx context.Context) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-c.frames:
		if !ok {
			return nil, c.exitError()
		}
		if f.err != nil {
			return nil, fmt.Errorf("read Claude stream: %w", f.err)
		}
		return f.data, nil
	}
}
func (c *connection) exitError() error {
	select {
	case <-c.done:
	default:
		return io.EOF
	}
	if c.waitErr != nil {
		return fmt.Errorf("Claude Code exited: %w%s", c.waitErr, c.stderrSuffix())
	}
	return io.EOF
}
func (c *connection) stderrSuffix() string {
	if s := c.stderr.String(); s != "" {
		return ": " + s
	}
	return ""
}

// close waits for normal native persistence first, then bounds cleanup and
// kills the process group through the shared process.Command cancellation.
func (c *connection) close() {
	c.closeOnce.Do(func() {
		c.in.Close()
		select {
		case <-c.done:
		case <-time.After(1500 * time.Millisecond):
			c.cancel()
		}
		c.cancel()
		// A launcher may exit before its children. The per-turn contract also
		// releases descendants still attached to the owned process group.
		_ = process.KillGroup(c.cmd)
		c.out.Close()
		<-c.done
	})
}

func (c *connection) initialize(ctx context.Context) (json.RawMessage, error) {
	if err := c.request("atlas-initialize", "initialize", map[string]any{"hooks": map[string]any{}}); err != nil {
		return nil, err
	}
	for {
		line, err := c.next(ctx)
		if err != nil {
			return nil, fmt.Errorf("Claude initialize: %w", err)
		}
		var v struct {
			Type     string `json:"type"`
			Response struct {
				Subtype string          `json:"subtype"`
				ID      string          `json:"request_id"`
				Error   string          `json:"error"`
				Data    json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if err = json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("invalid Claude initialization frame: %w", err)
		}
		if v.Type != "control_response" || v.Response.ID != "atlas-initialize" {
			continue
		}
		if v.Response.Subtype != "success" {
			return nil, fmt.Errorf("Claude initialize rejected: %s", v.Response.Error)
		}
		return v.Response.Data, nil
	}
}
