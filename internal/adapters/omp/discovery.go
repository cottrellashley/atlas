package omp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

type nativeHandle struct {
	Harness     string `json:"harness"`
	SessionID   string `json:"sessionId"`
	SessionFile string `json:"sessionFile"`
}

type candidate struct {
	path     string
	modified time.Time
}

func (d *Driver) Discover(ctx context.Context, cwd string, limit int) ([]harness.NativeSession, error) {
	if limit <= 0 {
		limit = 100
	}
	return d.discover(ctx, cwd, "", min(limit, 500))
}

// Find checks exact native IDs regardless of the recent-session browse limit.
func (d *Driver) Find(ctx context.Context, cwd, id string) ([]harness.NativeSession, error) {
	if id == "" {
		return []harness.NativeSession{}, nil
	}
	return d.discover(ctx, cwd, id, 0)
}

func (d *Driver) discover(ctx context.Context, cwd, id string, limit int) ([]harness.NativeSession, error) {
	root, explicit, err := d.sessionsRoot(cwd)
	if err != nil {
		return nil, err
	}
	files, err := candidates(ctx, root, explicit)
	if err != nil {
		return nil, fmt.Errorf("discover OMP sessions: %w", err)
	}
	sessions := []harness.NativeSession{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, err := readDescriptor(file.path, file.modified, cwd, id)
		if err != nil || session.ID == "" {
			continue // Ignore incomplete writes and unrelated JSONL files.
		}
		sessions = append(sessions, session)
		if id == "" && len(sessions) >= limit {
			break // Filter the native header before applying the browse cap.
		}
	}
	return sessions, nil
}

func candidates(ctx context.Context, root string, explicit bool) ([]candidate, error) {
	var files []candidate
	readFiles := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err == nil && info.Mode().IsRegular() {
				files = append(files, candidate{filepath.Join(dir, entry.Name()), info.ModTime()})
			}
		}
		return nil
	}
	if explicit {
		if err := readFiles(root); err != nil {
			return nil, err
		}
	} else {
		buckets, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, bucket := range buckets {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// OMP lists one level of project buckets. Do not recursively include
			// task-agent transcripts or follow a symlink outside the native store.
			if bucket.IsDir() {
				if err := readFiles(filepath.Join(root, bucket.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].path < files[j].path
		}
		return files[i].modified.After(files[j].modified)
	})
	return files, ctx.Err()
}

func readDescriptor(path string, modified time.Time, cwd, id string) (harness.NativeSession, error) {
	var session harness.NativeSession
	f, err := os.Open(path)
	if err != nil {
		return session, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return session, err
	}
	if !info.Mode().IsRegular() {
		return session, errors.New("OMP session is not a regular file")
	}
	head, err := io.ReadAll(io.NewSectionReader(f, 0, 64<<10))
	if err != nil {
		return session, err
	}
	first, rest, _ := bytes.Cut(head, []byte{'\n'})
	var header struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		CWD   string `json:"cwd"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(first, &header); err != nil {
		return session, err
	}
	var slotTitle *string
	if header.Type == "title" {
		title := header.Title
		slotTitle = &title
		first, _, _ = bytes.Cut(rest, []byte{'\n'})
		header = struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			CWD   string `json:"cwd"`
			Title string `json:"title"`
		}{}
		if err := json.Unmarshal(first, &header); err != nil {
			return session, err
		}
	}
	if header.Type != "session" || header.ID == "" || !filepath.IsAbs(header.CWD) {
		return session, errors.New("not an OMP native session")
	}
	if (id != "" && header.ID != id) || (cwd != "" && canonicalPath(header.CWD) != canonicalPath(cwd)) {
		return session, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return session, err
	}
	session = harness.NativeSession{ID: header.ID, CWD: header.CWD, Title: header.Title, UpdatedAt: modified, Handle: harness.Handle{Version: 1, Data: harness.JSON(nativeHandle{Harness: "omp", SessionID: header.ID, SessionFile: absolute})}}
	var prompt string
	consume := func(data []byte) {
		for len(data) > 0 {
			line, rest, _ := bytes.Cut(data, []byte{'\n'})
			data = rest
			var row struct {
				Type    string `json:"type"`
				Title   string `json:"title"`
				Model   string `json:"model"`
				Role    string `json:"role"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &row) != nil {
				continue
			}
			if row.Type == "title_change" && slotTitle == nil {
				session.Title = row.Title
			}
			if row.Type == "model_change" && (row.Role == "" || row.Role == "default") {
				session.Model = row.Model
			}
			if prompt == "" && row.Type == "message" && row.Message.Role == "user" {
				prompt = contentText(row.Message.Content)
			}
		}
	}
	consume(head)
	if info.Size() > int64(len(head)) {
		start := max(0, info.Size()-(256<<10))
		tail, err := io.ReadAll(io.NewSectionReader(f, start, 256<<10))
		if err != nil {
			return session, err
		}
		if start > 0 {
			var previous [1]byte
			if _, err := f.ReadAt(previous[:], start-1); err != nil {
				return session, err
			}
			if previous[0] != '\n' {
				_, tail, _ = bytes.Cut(tail, []byte{'\n'})
			}
		}
		consume(tail)
	}
	if slotTitle != nil {
		session.Title = *slotTitle // Mutable native slot wins over older audit entries.
	}
	if session.Title == "" {
		session.Title = strings.Join(strings.Fields(prompt), " ")
	}
	if session.Title == "" {
		session.Title = session.ID
	}
	if runes := []rune(session.Title); len(runes) > 160 {
		session.Title = string(runes[:160]) + "…"
	}
	return session, nil
}

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}
