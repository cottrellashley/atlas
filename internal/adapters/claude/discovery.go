package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

// Native history is only inspected for listing. Resume always uses Claude's
// native --resume path; Atlas never rewrites native transcript records.
func (d *driver) Discover(ctx context.Context, cwd string, limit int) ([]harness.NativeSession, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	root := d.projectsRoot()
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []harness.NativeSession{}, nil
	}
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var files []candidate
	for _, dir := range dirs {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !dir.IsDir() {
			continue
		}
		entries, e := os.ReadDir(filepath.Join(root, dir.Name()))
		if e != nil {
			continue
		}
		for _, f := range entries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") || !validUUID(strings.TrimSuffix(f.Name(), ".jsonl")) {
				continue
			}
			info, e := f.Info()
			if e != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, candidate{filepath.Join(root, dir.Name(), f.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if cwd != "" {
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return nil, err
		}
	}
	out := []harness.NativeSession{}
	for _, f := range files {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		s, e := readSummary(f.path, f.mod)
		if e != nil || s.CWD == "" {
			continue
		}
		if cwd != "" && !sameProjectPath(cwd, s.CWD) {
			continue
		}
		out = append(out, s)
		// Limit matching sessions, not candidates from unrelated workspaces.
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *driver) projectsRoot() string {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if v, ok := d.cfg.Env["CLAUDE_CONFIG_DIR"]; ok {
		configDir = v
	}
	if configDir == "" {
		home := d.cfg.Env["HOME"]
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		configDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(configDir, "projects")
}

func sameProjectPath(a, b string) bool {
	resolve := func(path string) string {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return filepath.Clean(path)
	}
	return resolve(a) == resolve(b)
}

func readSummary(path string, modified time.Time) (harness.NativeSession, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	s := harness.NativeSession{ID: id, UpdatedAt: modified}
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return s, err
	}
	const window = int64(256 << 10)
	read := func(offset, size int64) error {
		data, err := io.ReadAll(io.NewSectionReader(f, offset, size))
		if err != nil {
			return err
		}
		if offset > 0 {
			var previous [1]byte
			if _, err := f.ReadAt(previous[:], offset-1); err != nil {
				return err
			}
			if previous[0] != '\n' {
				_, data, _ = bytes.Cut(data, []byte{'\n'})
			}
		}
		for len(data) > 0 {
			line, rest, _ := bytes.Cut(data, []byte{'\n'})
			data = rest
			var row struct {
				Type      string `json:"type"`
				Sidechain bool   `json:"isSidechain"`
				CWD       string `json:"cwd"`
				Title     string `json:"customTitle"`
				Summary   string `json:"summary"`
				Message   struct {
					Model   string          `json:"model"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &row) != nil || row.Sidechain {
				continue
			}
			if row.CWD != "" {
				s.CWD = row.CWD
			}
			if row.Message.Model != "" {
				s.Model = row.Message.Model
			}
			if row.Title != "" {
				s.Title = row.Title
			} else if s.Title == "" && row.Type == "user" {
				var text string
				if json.Unmarshal(row.Message.Content, &text) != nil {
					var parts []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(row.Message.Content, &parts) == nil {
						for _, p := range parts {
							if p.Type == "text" {
								text += p.Text
							}
						}
					}
				}
				s.Title = preview(text)
			}
		}
		return nil
	}
	// Inspect at most two fixed windows. A truncated or oversized tool record is
	// skipped without throwing away metadata already found in other records.
	if err = read(0, min(stat.Size(), window)); err != nil {
		return s, err
	}
	if stat.Size() > window {
		offset := stat.Size() - window
		if err = read(offset, window); err != nil {
			return s, err
		}
	}
	if s.Title == "" {
		s.Title = id
	}
	s.Handle = *handle(id, s.CWD)
	return s, nil
}
func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:117]) + "..."
	}
	return s
}
