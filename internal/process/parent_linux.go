//go:build linux

package process

import (
	"os/exec"
	"syscall"
)

func parentDeath(cmd *exec.Cmd) { cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL }
