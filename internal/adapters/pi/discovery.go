package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func (d *Driver) env(key string) string {
	if value, ok := d.cfg.Env[key]; ok {
		return value
	}
	return os.Getenv(key)
}
func (d *Driver) home() string {
	if home := d.env("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}
func (d *Driver) configPath(value, cwd string) string {
	if value == "~" {
		return d.home()
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(d.home(), value[2:])
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}
func (d *Driver) sessionsDir(cwd string) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	explicit := d.opts.SessionDir
	if explicit == "" {
		for i := 1; i < len(d.cfg.Command); i++ {
			arg := d.cfg.Command[i]
			if arg == "--session-dir" && i+1 < len(d.cfg.Command) {
				explicit = d.cfg.Command[i+1]
				i++
			} else if strings.HasPrefix(arg, "--session-dir=") {
				explicit = strings.TrimPrefix(arg, "--session-dir=")
			}
		}
	}
	if explicit != "" {
		return d.configPath(explicit, cwd), nil
	}
	if value := d.env("PI_CODING_AGENT_SESSION_DIR"); value != "" {
		return d.configPath(value, cwd), nil
	}
	agentDir := d.env("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		agentDir = filepath.Join(d.home(), ".pi", "agent")
	} else {
		agentDir = d.configPath(agentDir, cwd)
	}
	var configured string
	for _, path := range []string{filepath.Join(agentDir, "settings.json"), filepath.Join(cwd, ".pi", "settings.json")} {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read Pi settings: %w", err)
		}
		var settings struct {
			SessionDir string `json:"sessionDir"`
		}
		if err = json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("parse Pi settings %s: %w", path, err)
		}
		if settings.SessionDir != "" {
			configured = settings.SessionDir
		}
	}
	if configured != "" {
		return d.configPath(configured, cwd), nil
	}
	return filepath.Join(agentDir, "sessions"), nil
}

func (d *Driver) Discover(ctx context.Context, cwd string, limit int) ([]harness.NativeSession, error) {
	root, err := d.sessionsDir(cwd)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	type candidate struct {
		path     string
		modified time.Time
	}
	var candidates []candidate
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{path: path, modified: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover Pi sessions: %w", err)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	if len(candidates) > 500 {
		candidates = candidates[:500]
	}
	sessions := make([]harness.NativeSession, 0, limit)
	for _, file := range candidates {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		session, readErr := readSessionDescriptor(file.path, file.modified)
		if readErr != nil {
			continue
		} // Concurrent append/truncation or non-session JSONL.
		if cwd != "" && !samePath(cwd, session.CWD) {
			continue
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func samePath(a, b string) bool {
	resolve := func(p string) string {
		v, err := filepath.Abs(p)
		if err == nil {
			p = v
		}
		v, err = filepath.EvalSymlinks(p)
		if err == nil {
			p = v
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}

func readSessionDescriptor(path string, modified time.Time) (harness.NativeSession, error) {
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
	head, err := io.ReadAll(io.LimitReader(f, 64*1024))
	if err != nil {
		return session, err
	}
	first, _, _ := bytes.Cut(head, []byte{'\n'})
	var header struct {
		Type      string    `json:"type"`
		ID        string    `json:"id"`
		CWD       string    `json:"cwd"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err = json.Unmarshal(first, &header); err != nil {
		return session, err
	}
	if header.Type != "session" || header.ID == "" || header.CWD == "" {
		return session, errors.New("not a Pi session")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return session, err
	}
	session = harness.NativeSession{ID: header.ID, CWD: header.CWD, UpdatedAt: modified, Handle: harness.Handle{Version: 1, Data: harness.JSON(nativeHandle{SessionID: header.ID, SessionFile: absolute})}}
	var firstPrompt, explicitTitle string
	consume := func(data []byte) {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxRecordBytes)
		for scanner.Scan() {
			var entry struct {
				Type      string    `json:"type"`
				Name      string    `json:"name"`
				Provider  string    `json:"provider"`
				ModelID   string    `json:"modelId"`
				Timestamp time.Time `json:"timestamp"`
				Message   struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			if entry.Type == "session_info" && entry.Name != "" {
				explicitTitle = entry.Name
			}
			if entry.Type == "model_change" && entry.ModelID != "" {
				session.Model = entry.Provider + "/" + entry.ModelID
			}
			if firstPrompt == "" && entry.Type == "message" && entry.Message.Role == "user" {
				firstPrompt = contentText(entry.Message.Content)
			}
			if !entry.Timestamp.IsZero() {
				session.UpdatedAt = entry.Timestamp
			}
		}
	}
	consume(head)
	if info.Size() > int64(len(head)) {
		start := info.Size() - 256*1024
		if start < 0 {
			start = 0
		}
		if _, err = f.Seek(start, io.SeekStart); err != nil {
			return session, err
		}
		tail, readErr := io.ReadAll(io.LimitReader(f, 256*1024))
		if readErr != nil {
			return session, readErr
		}
		if start > 0 {
			_, rest, ok := bytes.Cut(tail, []byte{'\n'})
			if ok {
				tail = rest
			} else {
				tail = nil
			}
		}
		consume(tail)
	}
	session.Title = explicitTitle
	if session.Title == "" {
		session.Title = strings.Join(strings.Fields(firstPrompt), " ")
	}
	if session.Title == "" {
		session.Title = header.ID
	}
	if len([]rune(session.Title)) > 160 {
		session.Title = string([]rune(session.Title)[:160]) + "…"
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
	var out strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}
