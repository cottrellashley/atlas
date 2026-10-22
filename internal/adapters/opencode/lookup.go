package opencode

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

// Find retrieves a native session directly, without restricting its age to the
// recent-history browser window. It never initializes a project or sends a turn.
func (d *driver) Find(ctx context.Context, cwd, id string) ([]harness.NativeSession, error) {
	if !strings.HasPrefix(id, "ses") {
		return []harness.NativeSession{}, nil
	}
	s, err := d.startIn(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var result value[nativeSession]
	if err := s.json(ctx, "GET", sessionPath(id), nil, &result); err != nil {
		var response *responseError
		if errors.As(err, &response) && response.status == http.StatusNotFound {
			return []harness.NativeSession{}, nil
		}
		return nil, err
	}
	n := result.Data
	if n.ID != id || n.Location.Directory == "" || n.ParentID != "" || n.Time.Archived != 0 {
		return []harness.NativeSession{}, nil
	}
	if cwd != "" {
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(cwd) != filepath.Clean(n.Location.Directory) {
			return []harness.NativeSession{}, nil
		}
	}
	return []harness.NativeSession{{ID: n.ID, Title: n.Title, CWD: n.Location.Directory, UpdatedAt: time.UnixMilli(n.Time.Updated), Handle: handleFor(n), Model: n.Model.String()}}, nil
}
