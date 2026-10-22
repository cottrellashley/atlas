// Package native browses conversations owned by installed harnesses and prepares
// interactive launches. It never creates Atlas session records or runs inference.
package native

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/cottrellashley/atlas/internal/adapters"
	"github.com/cottrellashley/atlas/internal/harness"
)

// Group preserves the configured harness identity, including aliases that use
// separate native stores. A failed harness does not hide other harnesses' history.
type Group struct {
	Harness  string                  `json:"harness"`
	Adapter  string                  `json:"adapter"`
	Sessions []harness.NativeSession `json:"sessions"`
	Error    string                  `json:"error,omitempty"`
}

// StartOptions are project defaults applied only to a new native conversation.
type StartOptions struct {
	Model          string
	Thinking       string
	NativeAgent    string
	PermissionMode string
}

// List discovers native sessions, newest first within each configured harness.
// Empty cwd means all projects. Limit applies to each harness and is capped at
// 500; zero selects 100. Probes run concurrently with a ten-second deadline each.
func List(ctx context.Context, configs []harness.Config, cwd string, limit int) []Group {
	return collect(ctx, configs, cwd, limit, "", false)
}

// Find looks up an exact native ID independently of the recent-history limit.
// Aliases remain separate so callers can detect ambiguous native identities.
func Find(ctx context.Context, configs []harness.Config, cwd, id string) []Group {
	return collect(ctx, configs, cwd, 500, id, true)
}

func collect(ctx context.Context, configs []harness.Config, cwd string, limit int, id string, find bool) []Group {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	groups := make([]Group, len(configs))
	workers := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, cfg := range configs {
		alias := cfg.ID
		if alias == "" {
			alias = cfg.Adapter
		}
		groups[i] = Group{Harness: alias, Adapter: cfg.Adapter, Sessions: []harness.NativeSession{}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case workers <- struct{}{}:
				defer func() { <-workers }()
			case <-ctx.Done():
				groups[i].Error = ctx.Err().Error()
				return
			}
			probe, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := probe.Err(); err != nil {
				groups[i].Error = err.Error()
				return
			}
			driver, err := adapters.New(cfg)
			if err == nil {
				var sessions []harness.NativeSession
				if find {
					finder, ok := driver.(interface {
						Find(context.Context, string, string) ([]harness.NativeSession, error)
					})
					if id == "" {
						err = fmt.Errorf("native session ID is required")
					} else if !ok {
						err = fmt.Errorf("%s exact session lookup: %w", cfg.Adapter, harness.ErrUnsupported)
					} else {
						sessions, err = finder.Find(probe, cwd, id)
					}
				} else {
					sessions, err = driver.Discover(probe, cwd, limit)
				}
				if err == nil {
					groups[i].Sessions = append(groups[i].Sessions, sessions...)
					sort.SliceStable(groups[i].Sessions, func(a, b int) bool {
						x, y := groups[i].Sessions[a], groups[i].Sessions[b]
						if x.UpdatedAt.Equal(y.UpdatedAt) {
							return x.ID < y.ID
						}
						return x.UpdatedAt.After(y.UpdatedAt)
					})
					seen := map[string]bool{}
					unique := groups[i].Sessions[:0]
					for _, session := range groups[i].Sessions {
						if !seen[session.ID] {
							unique = append(unique, session)
							seen[session.ID] = true
						}
					}
					groups[i].Sessions = unique
				}
			}
			if err != nil {
				groups[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return groups
}

// Args returns native interactive flags to append to the configured command.
// A nil session starts a new conversation; a non-nil session must come from
// this harness's discovery and must be launched in its saved CWD. Resuming does
// not override the session's native model with the configured default model.
func Args(cfg harness.Config, session *harness.NativeSession) ([]string, error) {
	return ArgsForCWD(cfg, "", session)
}

// ArgsForCWD lets native harnesses use their own project-specific session stores
// when explicitly starting fresh, while preserving the interactive handoff.
func ArgsForCWD(cfg harness.Config, cwd string, session *harness.NativeSession) ([]string, error) {
	if session != nil && (session.CWD == "" || !filepath.IsAbs(session.CWD)) {
		return nil, fmt.Errorf("native session %q requires an absolute working directory", session.ID)
	}
	if cwd == "" && session != nil {
		cwd = session.CWD
	}
	driver, err := adapters.New(cfg)
	if err != nil {
		return nil, err
	}
	if interactive, ok := driver.(interface {
		InteractiveArgsForCWD(string, *harness.NativeSession) ([]string, error)
	}); ok {
		return interactive.InteractiveArgsForCWD(cwd, session)
	}
	interactive, ok := driver.(interface {
		InteractiveArgs(*harness.NativeSession) ([]string, error)
	})
	if !ok {
		return nil, fmt.Errorf("%s interactive launch: %w", cfg.Adapter, harness.ErrUnsupported)
	}
	return interactive.InteractiveArgs(session)
}

// ArgsWithOptions extends Args with explicit project defaults for new sessions.
// Resume must use the saved native settings, so overrides on resume are rejected.
func ArgsWithOptions(cfg harness.Config, session *harness.NativeSession, options StartOptions) ([]string, error) {
	return ArgsWithOptionsForCWD(cfg, "", session, options)
}

func ArgsWithOptionsForCWD(cfg harness.Config, cwd string, session *harness.NativeSession, options StartOptions) ([]string, error) {
	if session != nil && options != (StartOptions{}) {
		return nil, fmt.Errorf("project defaults cannot override a resumed native session: %w", harness.ErrUnsupported)
	}
	if options.Model != "" {
		cfg.Model = options.Model
	}
	args, err := ArgsForCWD(cfg, cwd, session)
	if err != nil || session != nil {
		return args, err
	}
	driver, err := adapters.New(cfg)
	if err != nil {
		return nil, err
	}
	extra, ok := driver.(interface {
		InteractiveOptions(thinking, agent, permissionMode string) ([]string, error)
	})
	if !ok {
		return nil, fmt.Errorf("%s interactive options: %w", cfg.Adapter, harness.ErrUnsupported)
	}
	flags, err := extra.InteractiveOptions(options.Thinking, options.NativeAgent, options.PermissionMode)
	if err != nil {
		return nil, err
	}
	return append(args, flags...), nil
}
