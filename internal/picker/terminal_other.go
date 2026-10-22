//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package picker

import (
	"context"
	"errors"
	"io"
)

func chooseTerminal(context.Context, int, int, io.Writer, *model) (int, error) {
	return -1, errors.New("interactive selection is supported on Unix terminals; select an item explicitly")
}
