package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "pty-master")
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slaveFD, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	slave := os.NewFile(uintptr(slaveFD), "pty-slave")
	t.Cleanup(func() { slave.Close() })
	if err := unix.IoctlSetWinsize(slaveFD, unix.TIOCSWINSZ, &unix.Winsize{Row: 12, Col: 80}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func readUntil(t *testing.T, file *os.File, match string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var output strings.Builder
	buffer := make([]byte, 8192)
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			t.Fatal(err)
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(int(file.Fd()), buffer)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(buffer[:n])
		if strings.Contains(output.String(), match) {
			return output.String()
		}
	}
	t.Fatalf("terminal did not show %q; output=%q", match, output.String())
	return ""
}

func TestPTYSelectionAndTerminalRestore(t *testing.T) {
	master, slave := openPTY(t)
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		index int
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		index, err := Choose(ctx, slave, slave, "Continue a session", []Item{
			{Title: "Research one", Group: "Claude Code"},
			{Title: "Review code", Group: "OpenCode"},
			{Title: "Research two", Group: "Claude Code"},
		})
		done <- result{index, err}
	}()
	initial := readUntil(t, master, "3 of 3 matches")
	if !strings.Contains(initial, "Claude Code") || !strings.Contains(initial, "OpenCode") {
		t.Fatalf("group headings missing: %q", initial)
	}
	if _, err := io.WriteString(master, "research"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, master, "2 of 3 matches")
	// A size change is handled without needing another keypress.
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 8, Col: 60}); err != nil {
		t.Fatal(err)
	}
	readUntil(t, master, "2 of 3 matches")
	if _, err := io.WriteString(master, "\x1b[B\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.index != 2 {
			t.Fatalf("native session selection: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("selection did not finish")
	}
	readUntil(t, master, "\x1b[?1049l")
	restored, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(original, restored) {
		t.Fatalf("terminal state not restored before Choose returned: %v", err)
	}
	// The picker must leave no stdin reader competing with the native harness.
	if _, err := io.WriteString(master, "native input\n"); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, 1000); err != nil || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("input unavailable after handoff: %v", err)
	}
	buffer := make([]byte, 64)
	n, err := unix.Read(int(slave.Fd()), buffer)
	if err != nil || string(buffer[:n]) != "native input\n" {
		t.Fatalf("input consumed after handoff: %q, %v", buffer[:n], err)
	}
}

func TestPTYCancellationRestoresTerminal(t *testing.T) {
	for _, action := range []string{"escape", "ctrl-c", "context"} {
		t.Run(action, func(t *testing.T) {
			master, slave := openPTY(t)
			original, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := Choose(ctx, slave, slave, "Pick", []Item{{Title: "one"}})
				done <- err
			}()
			readUntil(t, master, "1 of 1 matches")
			want := ErrCanceled
			switch action {
			case "escape":
				_, err = io.WriteString(master, "\x1b")
			case "ctrl-c":
				_, err = io.WriteString(master, "\x03")
			case "context":
				cancel()
				want = context.Canceled
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("cancellation: got %v; want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not finish")
			}
			restored, err := term.GetState(int(slave.Fd()))
			if err != nil || !reflect.DeepEqual(original, restored) {
				t.Fatalf("terminal not restored after %s: %v", action, err)
			}
		})
	}
}

type failingTerminal struct {
	*os.File
	err error
}

func (f failingTerminal) Write([]byte) (int, error)       { return 0, f.err }
func (f failingTerminal) WriteString(string) (int, error) { return 0, f.err }

func TestPTYOutputErrorRestoresTerminal(t *testing.T) {
	_, slave := openPTY(t)
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("output failed")
	index, err := Choose(context.Background(), slave, failingTerminal{slave, want}, "Pick", []Item{{Title: "one"}})
	if index != -1 || !errors.Is(err, want) {
		t.Fatalf("output failure: %d, %v", index, err)
	}
	restored, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(original, restored) {
		t.Fatalf("terminal not restored after output error: %v", err)
	}
}
