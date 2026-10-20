//go:build !windows

package cli

import (
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
	"os"
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func handoff(cfg harness.Config, cwd string, args []string) error {
	command := cfg.Command
	if len(command) == 0 {
		command = []string{cfg.Adapter}
	}
	exe, e := process.ResolveExecutable(cfg, cwd)
	if e != nil {
		return e
	}
	if e = os.Chdir(cwd); e != nil {
		return e
	}
	return syscall.Exec(exe, append(append([]string{}, command...), args...), process.Environment(cfg.Env))
}
