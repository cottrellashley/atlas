//go:build linux

package process

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

// This exercises an abrupt owner death, where context cancellation and deferred
// cleanup cannot run. It intentionally tests the direct child guarantee only.
func TestNativeChildDiesWithOwner(t *testing.T) {
	owner := exec.Command(os.Args[0], "-test.run=^TestParentDeathHelper$")
	owner.Env = append(os.Environ(), "ATLAS_PROCESS_DEATH_HELPER=owner")
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	var childPID int
	select {
	case line := <-ready:
		childPID, err = strconv.Atoi(line)
		if err != nil || childPID <= 0 {
			t.Fatalf("owner did not report a child PID: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not start its child")
	}
	t.Cleanup(func() { _ = syscall.Kill(-childPID, syscall.SIGKILL) })
	if !nativeChildAlive(childPID) {
		t.Fatal("child exited before its owner was killed")
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for nativeChildAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("native child %d survived its owner's SIGKILL", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func nativeChildAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return !os.IsNotExist(err)
	}
	// A killed orphan can remain a zombie until the init process reaps it.
	_, state, ok := strings.Cut(string(data), ") ")
	return !ok || (!strings.HasPrefix(state, "Z ") && !strings.HasPrefix(state, "X "))
}

func TestParentDeathHelper(t *testing.T) {
	switch os.Getenv("ATLAS_PROCESS_DEATH_HELPER") {
	case "owner":
		cfg := harness.Config{Command: []string{os.Args[0]}, Env: map[string]string{"ATLAS_PROCESS_DEATH_HELPER": "child"}}
		child := Command(context.Background(), cfg, "", "-test.run=^TestParentDeathHelper$")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(30 * time.Second)
		_ = KillGroup(child)
		_ = child.Wait()
	case "child":
		time.Sleep(30 * time.Second)
	default:
		t.Skip("subprocess helper")
	}
}
