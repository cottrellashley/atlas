// Package omp integrates Oh My Pi's native session store and terminal UI.
// OMP's paths, title slots, model records, and resume flags differ from Pi.
package omp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
)

type options struct {
	SessionDir string  `json:"sessionDir"`
	Profile    *string `json:"profile"`
}

type Driver struct {
	cfg  harness.Config
	opts options
}

func New(cfg harness.Config) (harness.Driver, error) {
	if cfg.Adapter == "" {
		cfg.Adapter = "omp"
	}
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"omp"}
	}
	if cfg.Command[0] == "" {
		return nil, errors.New("OMP command is empty")
	}
	d := &Driver{cfg: cfg}
	for key, value := range cfg.Options {
		var target any
		switch key {
		case "sessionDir":
			target = &d.opts.SessionDir
		case "profile":
			target = &d.opts.Profile
		default:
			return nil, fmt.Errorf("unknown OMP option %q", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return nil, fmt.Errorf("OMP option %s: %w", key, err)
		}
	}
	if _, err := d.profile(); err != nil {
		return nil, err
	}
	if _, _, err := d.commandFlag("--session-dir"); err != nil {
		return nil, err
	}
	return d, nil
}

// Catalog reads OMP's own provider-qualified selectors and thinking levels.
func (d *Driver) Catalog(ctx context.Context, cwd string) (harness.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"models", "--json"}
	if _, found, _ := d.commandFlag("--profile"); d.opts.Profile != nil && !found {
		args = append([]string{"--profile", *d.opts.Profile}, args...)
	}
	cfg := d.cfg
	cfg.Command = append([]string{}, d.cfg.Command[:1]...)
	for i := 1; i < len(d.cfg.Command); i++ {
		arg := d.cfg.Command[i]
		if arg == "--session-dir" {
			i++
		} else if !strings.HasPrefix(arg, "--session-dir=") {
			cfg.Command = append(cfg.Command, arg)
		}
	}
	cmd := process.Command(ctx, cfg, cwd, args...)
	stdout := &boundedBuffer{limit: 16 << 20}
	// Diagnostics can contain provider credentials; do not copy native stderr
	// into an Atlas error. The native command remains available for diagnosis.
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return harness.Catalog{}, ctx.Err()
		}
		return harness.Catalog{}, fmt.Errorf("OMP model catalog: %w", err)
	}
	var catalog struct {
		Models []struct {
			ID            string   `json:"id"`
			Provider      string   `json:"provider"`
			Selector      string   `json:"selector"`
			Name          string   `json:"name"`
			ContextWindow int      `json:"contextWindow"`
			Thinking      []string `json:"thinking"`
		} `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		return harness.Catalog{}, fmt.Errorf("parse OMP model catalog: %w", err)
	}
	result := harness.Catalog{Models: []harness.Model{}, Source: "Oh My Pi models --json"}
	for _, model := range catalog.Models {
		id := model.Selector
		if id == "" && model.ID != "" && model.Provider != "" {
			id = model.Provider + "/" + model.ID
		}
		if id == "" {
			continue
		}
		result.Models = append(result.Models, harness.Model{ID: id, Name: model.Name, ContextWindow: model.ContextWindow, Thinking: model.Thinking, Default: id == d.cfg.Model})
	}
	for _, mode := range []string{"always-ask", "always-ask-cached", "write", "yolo"} {
		result.Modes = append(result.Modes, harness.Mode{ID: mode, Name: mode})
	}
	return result, nil
}

// OMP inference belongs to its native UI; the retired Atlas-managed RPC flow
// is intentionally not claimed compatible with Pi's different protocol.
func (d *Driver) Run(ctx context.Context, _ harness.RunRequest, _ harness.Emit, _ <-chan harness.Control) (harness.RunResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.RunResult{}, err
	}
	return harness.RunResult{}, fmt.Errorf("OMP uses native sessions; start with atlas new or atlas resume: %w", harness.ErrUnsupported)
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		return 0, errors.New("OMP catalog exceeds 16 MiB")
	}
	return b.buffer.Write(data)
}
