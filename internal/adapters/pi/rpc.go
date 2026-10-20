package pi

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

const maxRecordBytes = 16 * 1024 * 1024

type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	Raw     json.RawMessage `json:"-"`
}

type tailWriter struct {
	mu   sync.Mutex
	data []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, p...)
	if len(w.data) > 8192 {
		w.data = append([]byte(nil), w.data[len(w.data)-8192:]...)
	}
	return len(p), nil
}
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.data))
}

// A process has one stdout reader and one command writer. The caller owns all
// records, including responses that arrive interleaved with extension dialogs.
type rpcProcess struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	records   chan record
	done      chan struct{}
	stop      chan struct{}
	cancel    context.CancelFunc
	stderr    tailWriter
	waitErr   error // published by closing done
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func startRPC(cfg harness.Config, cwd string, args []string) (*rpcProcess, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &rpcProcess{records: make(chan record, 128), done: make(chan struct{}), stop: make(chan struct{}), cancel: cancel}
	p.cmd = process.Command(ctx, cfg, cwd, args...)
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		p.stdin.Close()
		cancel()
		return nil, err
	}
	p.cmd.Stderr = &p.stderr
	if err = p.cmd.Start(); err != nil {
		p.stdin.Close()
		stdout.Close()
		cancel()
		return nil, fmt.Errorf("start Pi RPC: %w", err)
	}
	go func() {
		defer close(p.done)
		defer close(p.records)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), maxRecordBytes)
		var readErr error
	readLoop:
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var r record
			if err := json.Unmarshal(line, &r); err != nil {
				readErr = fmt.Errorf("invalid Pi JSONL record: %w", err)
				p.cancel()
				break
			}
			r.Raw = append(json.RawMessage(nil), line...)
			select {
			case p.records <- r:
			case <-p.stop:
				break readLoop
			}
		}
		if scanner.Err() != nil {
			readErr = fmt.Errorf("read Pi JSONL: %w", scanner.Err())
			p.cancel()
		}
		waitErr := p.cmd.Wait()
		if readErr != nil {
			p.waitErr = readErr
		} else {
			p.waitErr = waitErr
		}
	}()
	return p, nil
}

func (p *rpcProcess) send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("write Pi RPC: %w", err)
	}
	return nil
}

func (p *rpcProcess) exitedError() error {
	<-p.done
	err := p.waitErr
	if err == nil {
		err = errors.New("Pi RPC exited before completion")
	}
	if detail := p.stderr.String(); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

func (p *rpcProcess) close() error {
	p.closeOnce.Do(func() { close(p.stop); _ = p.stdin.Close() })
	defer p.cancel()
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(3 * time.Second):
		_ = process.KillGroup(p.cmd)
		p.cancel()
		<-p.done
		return errors.New("Pi RPC did not shut down after stdin closed; terminated process group")
	}
}

func responseError(r record) error {
	if r.Success {
		return nil
	}
	if r.Error == "" {
		r.Error = "request rejected"
	}
	return fmt.Errorf("Pi %s: %s", r.Command, r.Error)
}
