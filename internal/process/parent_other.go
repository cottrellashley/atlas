//go:build !linux && !windows

package process

import "os/exec"

func parentDeath(cmd *exec.Cmd) {}
