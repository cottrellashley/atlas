package claude

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/harness"
)

// InteractiveArgs uses Claude's native terminal UI and native persistence.
func (d *driver) InteractiveArgs(session *harness.NativeSession) ([]string, error) {
	o := d.options
	if o.MaxTurns != 0 || o.MaxBudgetUSD != 0 {
		return nil, fmt.Errorf("Claude maxTurns and maxBudgetUSD apply only to managed print-mode runs; remove them from this harness configuration for interactive sessions: %w", harness.ErrUnsupported)
	}
	args := []string{}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	for _, dir := range o.AdditionalDirectories {
		args = append(args, "--add-dir", dir)
	}
	if o.SettingSources != nil {
		args = append(args, "--setting-sources", strings.Join(o.SettingSources, ","))
	}
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	if len(o.Settings) > 0 {
		args = append(args, "--settings", string(o.Settings))
	}
	if len(o.MCPConfig) > 0 {
		args = append(args, "--mcp-config", string(o.MCPConfig))
	}
	if session != nil {
		n, err := decodeHandle(&session.Handle)
		if err != nil {
			return nil, err
		}
		if session.ID != n.SessionID || n.CWD == "" || filepath.Clean(session.CWD) != filepath.Clean(n.CWD) {
			return nil, fmt.Errorf("Claude session identity or working directory differs from its native handle")
		}
		args = append(args, "--resume", n.SessionID)
	} else if d.cfg.Model != "" {
		args = append(args, "--model", d.cfg.Model)
	}
	return args, nil
}

func (d *driver) InteractiveOptions(thinking, agent, permissionMode string) ([]string, error) {
	args := []string{}
	if thinking != "" {
		switch thinking {
		case "low", "medium", "high", "xhigh", "max":
			args = append(args, "--effort", thinking)
		default:
			return nil, fmt.Errorf("invalid Claude thinking effort %q", thinking)
		}
	}
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	if permissionMode != "" {
		if permissionMode == "default" {
			permissionMode = "manual"
		}
		switch permissionMode {
		case "manual", "plan", "acceptEdits", "dontAsk", "auto":
		case "bypassPermissions":
			args = append(args, "--allow-dangerously-skip-permissions")
		default:
			return nil, fmt.Errorf("invalid Claude permission mode %q", permissionMode)
		}
		args = append(args, "--permission-mode", permissionMode)
	}
	return args, nil
}
