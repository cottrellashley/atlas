package store

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/project"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

type Record struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	ID             string               `json:"id"`
	Harness        string               `json:"harness"`
	Adapter        string               `json:"adapter"`
	CWD            string               `json:"cwd"`
	Agent          string               `json:"agent,omitempty"`
	Title          string               `json:"title"`
	State          string               `json:"state"`
	TurnID         string               `json:"turnId,omitempty"`
	Model          string               `json:"model,omitempty"`
	EffectiveModel string               `json:"effectiveModel,omitempty"`
	Thinking       string               `json:"thinking,omitempty"`
	NativeAgent    string               `json:"nativeAgent,omitempty"`
	PermissionMode string               `json:"permissionMode,omitempty"`
	TrustProject   bool                 `json:"trustProject,omitempty"`
	Resume         *harness.Handle      `json:"resume,omitempty"`
	Permissions    []harness.Permission `json:"permissions,omitempty"`
	Error          string               `json:"error,omitempty"`
	Seq            int64                `json:"seq"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      time.Time            `json:"updatedAt"`
	Archived       bool                 `json:"archived,omitempty"`
}

// RecoverEvents replays durable entries and discards only an incomplete final
// write. Corrupt complete records fail loudly; they are never silently skipped.
// The caller must own the daemon lock while repairing a tail.
func (s Store) RecoverEvents(id string) ([]harness.Event, error) {
	dir, e := s.Dir(id)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_RDWR, 0600)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64<<10)
	var offset int64
	out := []harness.Event{}
	var seq int64
	for {
		var line []byte
		for {
			fragment, err := reader.ReadSlice('\n')
			line = append(line, fragment...)
			if len(line) > 32<<20 {
				return nil, fmt.Errorf("event exceeds 32 MiB")
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			e = err
			break
		}
		if len(line) == 0 && e == io.EOF {
			break
		}
		if e != nil && e != io.EOF {
			return nil, e
		}
		var ev harness.Event
		err := json.Unmarshal(bytes.TrimSpace(line), &ev)
		if err != nil {
			if e == io.EOF {
				if err = f.Truncate(offset); err != nil {
					return nil, err
				}
				if err = f.Sync(); err != nil {
					return nil, err
				}
				break
			}
			return nil, fmt.Errorf("corrupt event at byte %d: %w", offset, err)
		}
		if ev.Seq <= seq {
			return nil, fmt.Errorf("event sequence is not increasing at byte %d", offset)
		}
		seq = ev.Seq
		out = append(out, ev)
		offset += int64(len(line))
		if e == io.EOF {
			if _, err = f.WriteAt([]byte{'\n'}, offset); err != nil {
				return nil, err
			}
			if err = f.Sync(); err != nil {
				return nil, err
			}
			break
		}
	}
	return out, nil
}

type Store struct{ Home string }

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (s Store) Dir(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("invalid session ID %q", id)
	}
	return filepath.Join(s.Home, "sessions", id), nil
}
func (s Store) Save(r Record) error {
	dir, e := s.Dir(r.ID)
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return project.Atomic(filepath.Join(dir, "record.json"), append(b, '\n'), 0600)
}
func (s Store) Load(id string) (Record, error) {
	var r Record
	dir, e := s.Dir(id)
	if e != nil {
		return r, e
	}
	b, e := os.ReadFile(filepath.Join(dir, "record.json"))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	if e == nil && r.SchemaVersion != 1 {
		e = fmt.Errorf("unsupported session schema %d", r.SchemaVersion)
	}
	return r, e
}
func (s Store) List() ([]Record, error) {
	items, e := os.ReadDir(filepath.Join(s.Home, "sessions"))
	if os.IsNotExist(e) {
		return []Record{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, d := range items {
		if !d.IsDir() || !idPattern.MatchString(d.Name()) {
			continue
		}
		r, e := s.Load(d.Name())
		if e != nil {
			return nil, fmt.Errorf("session %s: %w", d.Name(), e)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (s Store) Append(id string, e harness.Event) error {
	dir, err := s.Dir(id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	return ce
}
func (s Store) Events(id string, after int64, limit int) ([]harness.Event, error) {
	dir, e := s.Dir(id)
	if e != nil {
		return nil, e
	}
	f, e := os.Open(filepath.Join(dir, "events.jsonl"))
	if os.IsNotExist(e) {
		return []harness.Event{}, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := []harness.Event{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 65536), 32<<20)
	for scan.Scan() {
		var event harness.Event
		if e = json.Unmarshal(scan.Bytes(), &event); e != nil {
			return nil, fmt.Errorf("event log corrupt: %w", e)
		}
		if event.Seq > after {
			out = append(out, event)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, scan.Err()
}
