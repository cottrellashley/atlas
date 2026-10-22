package pi

import (
	"context"

	"github.com/cottrellashley/atlas/internal/harness"
)

// Find resolves a native ID across the configured session store, independently
// of the recent-session display limit. Header filtering keeps transcript reads
// bounded and avoids inspecting unrelated conversations.
func (d *Driver) Find(ctx context.Context, cwd, id string) ([]harness.NativeSession, error) {
	if id == "" {
		return []harness.NativeSession{}, nil
	}
	return d.discover(ctx, cwd, 1, id)
}
