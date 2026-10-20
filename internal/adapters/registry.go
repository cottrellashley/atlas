package adapters

import (
	"fmt"
	"github.com/cottrellashley/atlas/internal/adapters/claude"
	"github.com/cottrellashley/atlas/internal/adapters/opencode"
	"github.com/cottrellashley/atlas/internal/adapters/pi"
	"github.com/cottrellashley/atlas/internal/harness"
)

func New(c harness.Config) (harness.Driver, error) {
	switch c.Adapter {
	case "claude":
		return claude.New(c)
	case "pi":
		return pi.New(c)
	case "opencode":
		return opencode.New(c)
	default:
		return nil, fmt.Errorf("unsupported harness adapter %q", c.Adapter)
	}
}
