//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func chooseTerminal(ctx context.Context, inputFD, outputFD int, out io.Writer, m *model) (selected int, err error) {
	state, err := term.MakeRaw(inputFD)
	if err != nil {
		return -1, fmt.Errorf("prepare terminal: %w", err)
	}
	defer func() {
		_, outputErr := io.WriteString(out, "\x1b[0m\x1b[?2004l\x1b[?25h\x1b[?1049l")
		restoreErr := term.Restore(inputFD, state)
		err = errors.Join(err, outputErr, restoreErr)
		if err != nil {
			selected = -1
		}
	}()
	// Signals caught while raw mode is active must unwind the terminal too.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if _, err := io.WriteString(out, "\x1b[?1049h\x1b[?25l\x1b[?2004h"); err != nil {
		return -1, err
	}

	var keys decoder
	var lastInput time.Time
	previousWidth, previousHeight := 0, 0
	dirty := true
	buf := make([]byte, 4096)
	fds := []unix.PollFd{{Fd: int32(inputFD), Events: unix.POLLIN}}
	for {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		select {
		case <-signals:
			return -1, ErrCanceled
		default:
		}
		for {
			key, ok := keys.next(time.Since(lastInput) >= 120*time.Millisecond)
			if !ok {
				break
			}
			selection, done, err := m.update(key)
			if done {
				return selection, err
			}
			dirty = true
		}
		width, height, sizeErr := term.GetSize(outputFD)
		if sizeErr != nil || width <= 0 || height <= 0 {
			width, height = 80, 24
		}
		if dirty || width != previousWidth || height != previousHeight {
			if _, err := io.WriteString(out, m.render(width, height)); err != nil {
				return -1, err
			}
			previousWidth, previousHeight = width, height
			dirty = false
		}
		// Polling leaves no goroutine blocked on stdin after selection or context
		// cancellation. Native harnesses receive exclusive input ownership.
		n, err := unix.Poll(fds, 60)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return -1, fmt.Errorf("read terminal: %w", err)
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(inputFD, buf)
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			if err != nil {
				return -1, fmt.Errorf("read terminal: %w", err)
			}
			if n == 0 {
				return -1, io.EOF
			}
			keys.pending = append(keys.pending, buf[:n]...)
			lastInput = time.Now()
			// An unfinished paste must not grow memory without bounds.
			if len(keys.pending) > 1024*1024 {
				return -1, errors.New("terminal input exceeds 1 MiB")
			}
		} else if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return -1, io.EOF
		}
	}
}
