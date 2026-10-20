//go:build windows

package process

import "os/exec"

// Atlas is deployed inside WSL. Native Windows execution is not supported.
func own(cmd *exec.Cmd)             {}
func KillGroup(cmd *exec.Cmd) error { return cmd.Process.Kill() }
