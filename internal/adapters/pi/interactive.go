package pi

import (
	"fmt"
	"os"

	"github.com/cottrellashley/atlas/internal/harness"
)

// InteractiveArgs keeps native session files and invokes Pi's terminal UI.
func (d *Driver) InteractiveArgs(session *harness.NativeSession) ([]string, error) {
	// baseArgs begins with the RPC transport selector. The remaining flags also
	// apply to the interactive interface.
	args := append([]string{}, d.baseArgs()[2:]...)
	if session == nil {
		if d.cfg.Model != "" {
			args = append(args, "--model", d.cfg.Model)
		}
		return args, nil
	}
	n, err := decodeHandle(session.Handle)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(n.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("Pi resume file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Pi resume requires a regular native session file")
	}
	actual, err := readSessionDescriptor(n.SessionFile, info.ModTime())
	if err != nil {
		return nil, fmt.Errorf("Pi resume file: %w", err)
	}
	if n.SessionID != actual.ID || session.ID != actual.ID || !samePath(session.CWD, actual.CWD) {
		return nil, fmt.Errorf("Pi session identity or working directory differs from its native file")
	}
	return append(args, "--session", n.SessionFile), nil
}

func (d *Driver) InteractiveOptions(thinking, agent, permissionMode string) ([]string, error) {
	if agent != "" {
		return nil, fmt.Errorf("Pi has no native agent selector: %w", harness.ErrUnsupported)
	}
	if permissionMode != "" {
		return nil, fmt.Errorf("Pi has no native permission modes: %w", harness.ErrUnsupported)
	}
	if thinking != "" {
		return []string{"--thinking", thinking}, nil
	}
	return []string{}, nil
}
