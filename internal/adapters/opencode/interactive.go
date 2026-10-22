package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/cottrellashley/atlas/internal/harness"
)

// InteractiveArgs resumes with OpenCode's native session selector. OpenCode's
// terminal UI owns model selection; its v2 CLI has no initial --model flag.
func (d *driver) InteractiveArgs(session *harness.NativeSession) ([]string, error) {
	if len(d.permissions) > 0 {
		return nil, fmt.Errorf("OpenCode permissions adapter overrides apply only to managed runs; configure native OpenCode permissions for interactive sessions: %w", harness.ErrUnsupported)
	}
	if session == nil {
		if d.config.Model != "" {
			return nil, fmt.Errorf("OpenCode v2 has no interactive model flag; select the model in OpenCode's native configuration or UI: %w", harness.ErrUnsupported)
		}
		return []string{}, nil
	}
	n, err := decodeHandle(session.Handle)
	if err != nil {
		return nil, err
	}
	if session.ID != n.SessionID || filepath.Clean(session.CWD) != filepath.Clean(n.CWD) {
		return nil, fmt.Errorf("OpenCode session identity or working directory differs from its native handle")
	}
	return []string{"--session", n.SessionID}, nil
}

func (d *driver) InteractiveOptions(thinking, agent, permissionMode string) ([]string, error) {
	if thinking != "" || agent != "" || (permissionMode != "" && permissionMode != "default") {
		return nil, fmt.Errorf("OpenCode v2 cannot apply thinking, agent or permission-mode overrides through its interactive CLI; configure these in OpenCode's native configuration or UI: %w", harness.ErrUnsupported)
	}
	return []string{}, nil
}
