package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/cottrellashley/atlas/internal/harness"
)

// Find resolves an exact native ID without restricting the search to recent
// sessions. Claude stores each transcript under its UUID filename.
func (d *driver) Find(ctx context.Context, cwd, id string) ([]harness.NativeSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []harness.NativeSession{}
	if !validUUID(id) {
		return out, nil
	}
	root := d.projectsRoot()
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !dir.IsDir() {
			continue
		}
		path := filepath.Join(root, dir.Name(), id+".jsonl")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		session, err := readSummary(path, info.ModTime())
		if err != nil || session.CWD == "" || (cwd != "" && !sameProjectPath(cwd, session.CWD)) {
			continue
		}
		out = append(out, session)
	}
	return out, nil
}
