package omp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/harness"
)

func (d *Driver) InteractiveArgs(session *harness.NativeSession) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if session != nil {
		cwd = session.CWD
	}
	return d.InteractiveArgsForCWD(cwd, session)
}

// InteractiveArgsForCWD creates a fresh native conversation even when the user
// has enabled OMP's implicit autoResume setting. The native per-project bucket
// is supplied explicitly; no global setting or transcript is rewritten.
func (d *Driver) InteractiveArgsForCWD(cwd string, session *harness.NativeSession) ([]string, error) {
	for _, arg := range d.cfg.Command[1:] {
		name, _, _ := strings.Cut(arg, "=")
		switch name {
		case "--continue", "-c", "--resume", "-r", "--restore", "--fork", "--no-session", "--goal", "--mode", "--print", "-p", "--cwd", "--from-claude", "--from-codex":
			return nil, fmt.Errorf("OMP configured command contains %s; Atlas needs to select the native session and working directory", name)
		}
	}
	args := []string{}
	if _, found, _ := d.commandFlag("--profile"); d.opts.Profile != nil && !found {
		args = append(args, "--profile", *d.opts.Profile)
	}
	if session == nil {
		dir, err := d.projectSessionDir(cwd)
		if err != nil {
			return nil, err
		}
		// Pass an absolute path even when the configured session directory was
		// relative, so discovery and handoff resolve the same native store.
		if _, found, _ := d.commandFlag("--session-dir"); !found {
			args = append(args, "--session-dir", dir)
		}
		if d.cfg.Model != "" {
			args = append(args, "--model", d.cfg.Model)
		}
		return args, nil
	}
	var handle nativeHandle
	if session.Handle.Version != 1 || json.Unmarshal(session.Handle.Data, &handle) != nil || handle.Harness != "omp" || !filepath.IsAbs(handle.SessionFile) {
		return nil, fmt.Errorf("OMP resume requires an OMP native session handle with an absolute file")
	}
	info, err := os.Lstat(handle.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("OMP resume file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OMP resume requires a regular native session file")
	}
	actual, err := readDescriptor(handle.SessionFile, info.ModTime(), "", "")
	if err != nil {
		return nil, fmt.Errorf("OMP resume file: %w", err)
	}
	if handle.SessionID != actual.ID || session.ID != actual.ID || canonicalPath(session.CWD) != canonicalPath(actual.CWD) || canonicalPath(cwd) != canonicalPath(actual.CWD) {
		return nil, fmt.Errorf("OMP session identity or working directory differs from its native file")
	}
	if d.opts.SessionDir != "" {
		if _, found, _ := d.commandFlag("--session-dir"); !found {
			root, _, err := d.sessionsRoot(cwd)
			if err != nil {
				return nil, err
			}
			args = append(args, "--session-dir", root)
		}
	}
	return append(args, "--resume", handle.SessionFile), nil
}

func (d *Driver) InteractiveOptions(thinking, agent, permissionMode string) ([]string, error) {
	if agent != "" {
		return nil, fmt.Errorf("OMP agents are task subagents; no top-level agent selector: %w", harness.ErrUnsupported)
	}
	args := []string{}
	if thinking != "" {
		switch thinking {
		case "off", "minimal", "low", "medium", "high", "xhigh", "max", "auto":
			args = append(args, "--thinking", thinking)
		default:
			return nil, fmt.Errorf("invalid OMP thinking level %q", thinking)
		}
	}
	if permissionMode != "" {
		switch permissionMode {
		case "always-ask", "always-ask-cached", "write", "yolo":
			args = append(args, "--approval-mode", permissionMode)
		default:
			return nil, fmt.Errorf("invalid OMP permission mode %q", permissionMode)
		}
	}
	return args, nil
}
