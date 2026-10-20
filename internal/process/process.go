// Package process centralizes shell-free launch and process-tree ownership.
package process

import (
	"context"
	"github.com/cottrellashley/atlas/internal/harness"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func Environment(overrides map[string]string) []string {
	env := map[string]string{}
	for _, value := range os.Environ() {
		k, v, ok := strings.Cut(value, "=")
		if ok {
			env[k] = v
		}
	}
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_SSE_PORT", "CLAUDE_AGENT_SDK_VERSION"} {
		delete(env, k)
	}
	for k, v := range overrides {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func Command(ctx context.Context, cfg harness.Config, cwd string, args ...string) *exec.Cmd {
	command := cfg.Command
	if len(command) == 0 {
		command = []string{cfg.Adapter}
	}
	argv := append(append([]string{}, command[1:]...), args...)
	path, err := ResolveExecutable(cfg, cwd)
	if err != nil {
		path = command[0]
	}
	cmd := exec.CommandContext(ctx, path, argv...)
	if err != nil {
		cmd.Err = err
	}
	cmd.Dir = cwd
	cmd.Env = Environment(cfg.Env)
	cmd.WaitDelay = 3 * time.Second
	own(cmd)
	return cmd
}

// ResolveExecutable uses the child's effective PATH, without mutating the
// parent environment (which would race concurrent adapter launches).
func ResolveExecutable(cfg harness.Config, cwd string) (string, error) {
	name := cfg.Adapter
	if len(cfg.Command) > 0 {
		name = cfg.Command[0]
	}
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			return filepath.Abs(filepath.Join(cwd, name))
		}
		return name, nil
	}
	path := os.Getenv("PATH")
	if configured, ok := cfg.Env["PATH"]; ok {
		path = configured
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		p, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
