// Package install reconciles native harness resources without taking ownership
// of unrelated user configuration. Plans are optimistic transactions: Apply
// checks every input again under a process lock before making any changes.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const receiptPath = ".atlas/install-receipt.json"
const journalPath = ".atlas/install-journal.json"
const lockPath = ".atlas/install.lock"

type File struct {
	Path string      `json:"path"`
	Data []byte      `json:"data"`
	Mode fs.FileMode `json:"mode,omitempty"`
}

// Value owns an object member addressed by an RFC 6901 JSON pointer. Array
// owns one exact element of the array at Pointer instead of the whole member.
// Array elements are deduplicated and can have several package owners.
type Value struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
	Value   any    `json:"value"`
	Array   bool   `json:"array,omitempty"`
}

type Contribution struct {
	PackageID string            `json:"packageId"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Files     []File            `json:"files,omitempty"`
	Values    []Value           `json:"values,omitempty"`
}

type Change struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	BeforeHash string `json:"beforeHash,omitempty"`
	AfterHash  string `json:"afterHash,omitempty"`
}

// Transaction deliberately hides file contents from JSON previews. It must be
// applied in the process that planned it, to the same absolute root.
type Transaction struct {
	Files   []Change `json:"files"`
	root    string
	entries []entry
}

type Drift struct {
	Path     string   `json:"path"`
	Pointer  string   `json:"pointer,omitempty"`
	Packages []string `json:"packages"`
	Reason   string   `json:"reason"`
}

type snapshot struct {
	Exists bool        `json:"exists"`
	Data   []byte      `json:"data,omitempty"`
	Mode   fs.FileMode `json:"mode,omitempty"`
}
type entry struct {
	Path   string   `json:"path"`
	Before snapshot `json:"before"`
	After  snapshot `json:"after"`
}
type resource struct {
	Kind           string          `json:"kind"`
	Path           string          `json:"path"`
	Pointer        string          `json:"pointer,omitempty"`
	Data           []byte          `json:"data,omitempty"`
	Mode           fs.FileMode     `json:"mode,omitempty"`
	Value          json.RawMessage `json:"value,omitempty"`
	Owners         []string        `json:"owners"`
	Original       snapshot        `json:"original,omitempty"`
	OriginalValue  json.RawMessage `json:"originalValue,omitempty"`
	OriginalExists bool            `json:"originalExists,omitempty"`
}
type document struct {
	Existed bool            `json:"existed"`
	Created map[string]bool `json:"created,omitempty"`
}
type receipt struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Contributions []Contribution      `json:"contributions"`
	Resources     map[string]resource `json:"resources"`
	Documents     map[string]document `json:"documents"`
}
type journal struct {
	SchemaVersion int     `json:"schemaVersion"`
	Committed     bool    `json:"committed"`
	Entries       []entry `json:"entries"`
}

func emptyReceipt() receipt {
	return receipt{SchemaVersion: 1, Resources: map[string]resource{}, Documents: map[string]document{}}
}

func rootPath(root string) (string, error) {
	a, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	real, e := filepath.EvalSymlinks(a)
	if e != nil {
		return "", e
	}
	if a != real {
		return "", fmt.Errorf("install root contains a symlink: %s", a)
	}
	i, e := os.Stat(a)
	if e != nil {
		return "", e
	}
	if !i.IsDir() {
		return "", errors.New("install root is not a directory")
	}
	return a, nil
}
func safe(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("unsafe install path %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != rel {
		return "", fmt.Errorf("unsafe install path %q", rel)
	}
	p := root
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		i, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if e == nil && i.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink in install path %s", rel)
		}
	}
	return p, nil
}
func reserved(path string) bool {
	return path == receiptPath || path == journalPath || path == lockPath || strings.HasPrefix(path, ".atlas/.install-")
}
func read(root, path string) (snapshot, error) {
	p, e := safe(root, path)
	if e != nil {
		return snapshot{}, e
	}
	i, e := os.Stat(p)
	if os.IsNotExist(e) {
		return snapshot{}, nil
	}
	if e != nil {
		return snapshot{}, e
	}
	if !i.Mode().IsRegular() {
		return snapshot{}, fmt.Errorf("install path is not a regular file: %s", path)
	}
	if i.Size() > 64<<20 {
		return snapshot{}, fmt.Errorf("install file too large: %s", path)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return snapshot{}, e
	}
	return snapshot{true, b, i.Mode().Perm()}, nil
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func equal(a, b snapshot) bool {
	return a.Exists == b.Exists && (!a.Exists || a.Mode == b.Mode && bytes.Equal(a.Data, b.Data))
}
func encode(v any) []byte { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
func loadReceipt(root string) (receipt, snapshot, error) {
	s, e := read(root, receiptPath)
	if e != nil {
		return receipt{}, s, e
	}
	if !s.Exists {
		return emptyReceipt(), s, nil
	}
	var r receipt
	dec := json.NewDecoder(bytes.NewReader(s.Data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if e = dec.Decode(&r); e != nil {
		return r, s, fmt.Errorf("invalid install receipt: %w", e)
	}
	if e = dec.Decode(new(any)); e != io.EOF {
		return r, s, errors.New("invalid trailing content in install receipt")
	}
	if r.SchemaVersion != 1 || r.Resources == nil || r.Documents == nil {
		return r, s, errors.New("unsupported or incomplete install receipt")
	}
	for k, v := range r.Resources {
		if _, e = safe(root, v.Path); e != nil {
			return r, s, e
		}
		if reserved(v.Path) {
			return r, s, errors.New("receipt contains a reserved resource path")
		}
		if v.Kind != "file" && v.Kind != "value" && v.Kind != "array" {
			return r, s, errors.New("receipt contains unknown resource kind")
		}
		if k != resourceKey(v) {
			return r, s, errors.New("receipt resource identity mismatch")
		}
	}
	return r, s, nil
}

// Contributions returns the previously installed desired snapshot, suitable
// for replacing one package before calling Plan. Its data is private local state.
func Contributions(root string) ([]Contribution, error) {
	a, e := rootPath(root)
	if e != nil {
		return nil, e
	}
	r, _, e := loadReceipt(a)
	return r.Contributions, e
}

func resourceKey(r resource) string {
	k := r.Kind + ":" + r.Path + ":" + r.Pointer
	if r.Kind == "array" {
		// Receipt pretty-printing changes RawMessage whitespace. Identity follows
		// the canonical JSON value, never its serialized indentation.
		value, e := canonical(r.Value)
		if e != nil {
			value = r.Value
		}
		k += ":" + hash(value)
	}
	return k
}
func canonical(v any) (json.RawMessage, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var raw any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&raw); e != nil {
		return nil, e
	}
	b, e = json.Marshal(raw)
	return b, e
}
func aggregate(root string, cs []Contribution) (map[string]resource, error) {
	out := map[string]resource{}
	packages := map[string]bool{}
	add := func(r resource, owner string) error {
		if _, e := safe(root, r.Path); e != nil {
			return e
		}
		if reserved(r.Path) {
			return fmt.Errorf("reserved install path %s", r.Path)
		}
		k := resourceKey(r)
		if old, ok := out[k]; ok {
			if !bytes.Equal(old.Data, r.Data) || old.Mode != r.Mode || !bytes.Equal(old.Value, r.Value) {
				return fmt.Errorf("packages disagree on resource %s%s", r.Path, r.Pointer)
			}
			for _, x := range old.Owners {
				if x == owner {
					return nil
				}
			}
			old.Owners = append(old.Owners, owner)
			sort.Strings(old.Owners)
			out[k] = old
			return nil
		}
		r.Owners = []string{owner}
		out[k] = r
		return nil
	}
	for _, c := range cs {
		if c.PackageID == "" || packages[c.PackageID] {
			return nil, fmt.Errorf("empty or repeated package identity %q", c.PackageID)
		}
		packages[c.PackageID] = true
		for _, f := range c.Files {
			m := f.Mode.Perm()
			if m == 0 {
				m = 0644
			}
			if e := add(resource{Kind: "file", Path: f.Path, Data: f.Data, Mode: m}, c.PackageID); e != nil {
				return nil, e
			}
		}
		for _, v := range c.Values {
			if _, e := pointerParts(v.Pointer); e != nil {
				return nil, e
			}
			b, e := canonical(v.Value)
			if e != nil {
				return nil, e
			}
			kind := "value"
			if v.Array {
				kind = "array"
			}
			if e = add(resource{Kind: kind, Path: v.Path, Pointer: v.Pointer, Value: b}, c.PackageID); e != nil {
				return nil, e
			}
		}
	}
	keys := sortedKeys(out)
	for i, k := range keys {
		a := out[k]
		for _, other := range keys[i+1:] {
			b := out[other]
			if a.Path != b.Path {
				continue
			}
			if a.Kind == "file" || b.Kind == "file" || a.Pointer == b.Pointer && (a.Kind != "array" || b.Kind != "array") || strings.HasPrefix(a.Pointer, b.Pointer+"/") || strings.HasPrefix(b.Pointer, a.Pointer+"/") {
				return nil, fmt.Errorf("overlapping package resources at %s (%s, %s)", a.Path, a.Pointer, b.Pointer)
			}
		}
	}
	return out, nil
}
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Plan computes a complete desired installation. Omitting a previous package
// removes its owned resources. Metadata files (for declarations and lockfiles)
// join the same transaction but are not package-owned; their current contents
// are still checked at Apply. Planning never changes the filesystem.
func Plan(root string, desired []Contribution, metadata ...File) (*Transaction, error) {
	return plan(root, nil, desired, metadata...)
}

// PlanWithBase applies a project template virtually before reconciling packages.
// Its single transaction writes the template, all package resources, metadata,
// and receipt. Package removal later restores this template baseline. No base
// files are written when package validation or conflict detection fails.
func PlanWithBase(root string, base []File, desired []Contribution, metadata ...File) (*Transaction, error) {
	return plan(root, base, desired, metadata...)
}

func plan(root string, base []File, desired []Contribution, metadata ...File) (*Transaction, error) {
	a, e := rootPath(root)
	if e != nil {
		return nil, e
	}
	if j, e := read(a, journalPath); e != nil {
		return nil, e
	} else if j.Exists {
		return nil, errors.New("unfinished installation: run package recovery before planning")
	}
	old, oldReceipt, e := loadReceipt(a)
	if e != nil {
		return nil, e
	}
	want, e := aggregate(a, desired)
	if e != nil {
		return nil, e
	}
	for _, previous := range old.Resources {
		for _, replacement := range want {
			if previous.Path == replacement.Path && (previous.Kind == "file") != (replacement.Kind == "file") {
				return nil, fmt.Errorf("resource %s changes between whole-file and configuration ownership; remove it before changing its kind", previous.Path)
			}
		}
	}
	current := map[string]snapshot{}
	actualBase := map[string]snapshot{}
	for _, f := range base {
		if reserved(f.Path) {
			return nil, fmt.Errorf("reserved base path %s", f.Path)
		}
		if _, exists := actualBase[f.Path]; exists {
			return nil, fmt.Errorf("duplicate base file %s", f.Path)
		}
		before, e := read(a, f.Path)
		if e != nil {
			return nil, e
		}
		actualBase[f.Path] = before
		mode := f.Mode.Perm()
		if mode == 0 {
			mode = 0644
		}
		if before.Exists {
			mode = before.Mode
		}
		current[f.Path] = snapshot{Exists: true, Data: append([]byte(nil), f.Data...), Mode: mode}
	}
	for _, rs := range []map[string]resource{old.Resources, want} {
		for _, r := range rs {
			if _, ok := current[r.Path]; !ok {
				s, e := read(a, r.Path)
				if e != nil {
					return nil, e
				}
				current[r.Path] = s
			}
		}
	}
	for _, r := range old.Resources {
		if reason := checkResource(r, current[r.Path]); reason != "" {
			return nil, fmt.Errorf("managed resource changed: %s%s: %s", r.Path, r.Pointer, reason)
		}
	}
	next := emptyReceipt()
	next.Contributions = desired
	next.Resources = want
	work := map[string]snapshot{}
	for path, s := range current {
		work[path] = s
	}
	docs := map[string]configurationDocument{}
	for _, rs := range []map[string]resource{old.Resources, want} {
		for _, r := range rs {
			if r.Kind == "file" {
				continue
			}
			if _, ok := docs[r.Path]; ok {
				continue
			}
			s := current[r.Path]
			data := s.Data
			if !s.Exists {
				data = []byte("{}\n")
			}
			d, e := parseConfiguration(r.Path, data)
			if e != nil {
				return nil, fmt.Errorf("%s: %w", r.Path, e)
			}
			docs[r.Path] = d
			state, ok := old.Documents[r.Path]
			if !ok {
				state = document{Existed: s.Exists, Created: map[string]bool{}}
			}
			if state.Created == nil {
				state.Created = map[string]bool{}
			}
			next.Documents[r.Path] = state
		}
	}
	// Remove retired resources first. Originals are restored only after checking
	// that the installed value has not been changed by the user.
	for _, key := range sortedKeys(old.Resources) {
		r := old.Resources[key]
		if _, ok := want[key]; ok {
			continue
		}
		if r.Kind == "file" {
			work[r.Path] = r.Original
			continue
		}
		d := docs[r.Path]
		if r.Kind == "array" {
			if !r.OriginalExists {
				e = d.array(r.Pointer, r.Value, false)
			}
		} else if r.OriginalExists {
			_, e = d.set(r.Pointer, r.OriginalValue)
		} else {
			e = d.remove(r.Pointer)
		}
		if e != nil {
			return nil, fmt.Errorf("remove %s%s: %w", r.Path, r.Pointer, e)
		}
	}
	for _, key := range sortedKeys(want) {
		r := want[key]
		prior, managed := old.Resources[key]
		if managed {
			r.Original = prior.Original
			r.OriginalValue = prior.OriginalValue
			r.OriginalExists = prior.OriginalExists
		}
		if r.Kind == "file" {
			s := work[r.Path]
			if !managed {
				if s.Exists && !bytes.Equal(s.Data, r.Data) {
					return nil, fmt.Errorf("existing file conflicts with package: %s", r.Path)
				}
				r.Original = s
			}
			mode := r.Mode
			if s.Exists {
				mode = s.Mode
			}
			r.Mode = mode
			work[r.Path] = snapshot{true, r.Data, mode}
		} else {
			d := docs[r.Path]
			v, exists, e := d.get(r.Pointer)
			if e != nil {
				return nil, e
			}
			if r.Kind == "array" {
				if !managed {
					r.OriginalExists = exists && arrayContains(v, r.Value)
				}
				if !exists {
					created, e := d.set(r.Pointer, json.RawMessage("[]"))
					if e != nil {
						return nil, e
					}
					state := next.Documents[r.Path]
					for _, p := range created {
						state.Created[p] = true
					}
					state.Created[r.Pointer] = true
					next.Documents[r.Path] = state
				}
				e = d.array(r.Pointer, r.Value, true)
			} else {
				if !managed {
					if exists && !jsonEqual(v, r.Value) {
						return nil, fmt.Errorf("existing configuration conflicts with package: %s%s", r.Path, r.Pointer)
					}
					r.OriginalExists = exists
					r.OriginalValue = v
				}
				created, err := d.set(r.Pointer, r.Value)
				e = err
				state := next.Documents[r.Path]
				for _, p := range created {
					state.Created[p] = true
				}
				next.Documents[r.Path] = state
			}
			if e != nil {
				return nil, fmt.Errorf("install %s%s: %w", r.Path, r.Pointer, e)
			}
		}
		next.Resources[key] = r
	}
	for path, d := range docs {
		state := next.Documents[path]
		parents := sortedKeys(state.Created)
		sort.Slice(parents, func(i, j int) bool { return len(parents[i]) > len(parents[j]) })
		for _, p := range parents {
			ownedMember := false
			for _, r := range want {
				if r.Path == path && r.Pointer == p {
					ownedMember = true
					break
				}
			}
			if ownedMember {
				continue
			}
			v, exists, e := d.get(p)
			if e != nil {
				return nil, e
			}
			if !exists {
				delete(state.Created, p)
				continue
			}
			if jsonEqual(v, []byte("{}")) || jsonEqual(v, []byte("[]")) {
				if e = d.remove(p); e != nil {
					return nil, e
				}
				delete(state.Created, p)
			}
		}
		owned := false
		for _, r := range want {
			if r.Path == path && r.Kind != "file" {
				owned = true
				break
			}
		}
		if !state.Existed && !owned && d.empty() {
			work[path] = snapshot{}
		} else {
			s := current[path]
			s.Exists = true
			if s.Mode == 0 {
				s.Mode = 0600
			}
			s.Data, e = d.bytes()
			if e != nil {
				return nil, fmt.Errorf("%s: %w", path, e)
			}
			work[path] = s
		}
		if owned {
			next.Documents[path] = state
		} else {
			delete(next.Documents, path)
		}
	}
	for _, f := range metadata {
		if reserved(f.Path) {
			return nil, fmt.Errorf("reserved metadata path %s", f.Path)
		}
		if _, ok := current[f.Path]; ok {
			return nil, fmt.Errorf("metadata overlaps resource %s", f.Path)
		}
		s, e := read(a, f.Path)
		if e != nil {
			return nil, e
		}
		current[f.Path] = s
		mode := f.Mode.Perm()
		if mode == 0 {
			mode = 0600
		}
		if s.Exists {
			mode = s.Mode
		}
		work[f.Path] = snapshot{true, f.Data, mode}
	}
	current[receiptPath] = oldReceipt
	work[receiptPath] = snapshot{true, encode(next), 0600}
	t := &Transaction{root: a, Files: []Change{}}
	for _, path := range sortedKeys(work) {
		before, after := current[path], work[path]
		if actual, ok := actualBase[path]; ok {
			before = actual
		}
		after.Data = append([]byte(nil), after.Data...)
		t.entries = append(t.entries, entry{path, before, after})
		if !equal(before, after) {
			action := "update"
			if !before.Exists {
				action = "create"
			}
			if !after.Exists {
				action = "remove"
			}
			c := Change{Path: path, Action: action}
			if before.Exists {
				c.BeforeHash = hash(before.Data)
			}
			if after.Exists {
				c.AfterHash = hash(after.Data)
			}
			t.Files = append(t.Files, c)
		}
	}
	return t, nil
}

func checkResource(r resource, s snapshot) string {
	if !s.Exists {
		return "file is missing"
	}
	if r.Kind == "file" {
		if !bytes.Equal(r.Data, s.Data) {
			return "file contents differ"
		}
		if r.Mode != s.Mode {
			return "file permissions differ"
		}
		return ""
	}
	d, e := parseConfiguration(r.Path, s.Data)
	if e != nil {
		return "configuration is invalid: " + e.Error()
	}
	v, ok, e := d.get(r.Pointer)
	if e != nil {
		return e.Error()
	}
	if !ok {
		return "configuration key is missing"
	}
	if r.Kind == "array" {
		if !arrayContains(v, r.Value) {
			return "array entry is missing"
		}
	} else if !jsonEqual(v, r.Value) {
		return "configuration value differs"
	}
	return ""
}

// Status checks owned resources only; unrelated user settings are not drift.
func Status(root string) ([]Drift, error) {
	a, e := rootPath(root)
	if e != nil {
		return nil, e
	}
	r, _, e := loadReceipt(a)
	if e != nil {
		return nil, e
	}
	out := []Drift{}
	for _, k := range sortedKeys(r.Resources) {
		v := r.Resources[k]
		s, e := read(a, v.Path)
		if e != nil {
			return nil, e
		}
		if reason := checkResource(v, s); reason != "" {
			out = append(out, Drift{v.Path, v.Pointer, v.Owners, reason})
		}
	}
	return out, nil
}

func lock(root string) (func(), error) {
	p, e := safe(root, ".atlas")
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return nil, e
	}
	p, e = safe(root, lockPath)
	if e != nil {
		return nil, e
	}
	fd, e := syscall.Open(p, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("another package operation holds the install lock: %w", e)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = syscall.Close(fd) }, nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func write(root, path string, s snapshot) error {
	p, e := safe(root, path)
	if e != nil {
		return e
	}
	if !s.Exists {
		e = os.Remove(p)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		return syncDir(filepath.Dir(p))
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	// Recheck after creating parents; never follow a preexisting symlink.
	if _, e = safe(root, path); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".install-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(s.Mode.Perm()); e == nil {
		_, e = f.Write(s.Data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, p); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func rollback(root string, j journal) error {
	for _, en := range j.Entries {
		s, e := read(root, en.Path)
		if e != nil {
			return e
		}
		if !equal(s, en.Before) && !equal(s, en.After) {
			return fmt.Errorf("recovery stopped: %s changed after the interrupted installation", en.Path)
		}
	}
	for i := len(j.Entries) - 1; i >= 0; i-- {
		en := j.Entries[i]
		if e := write(root, en.Path, en.Before); e != nil {
			return e
		}
	}
	return nil
}
func recoverLocked(root string) error {
	s, e := read(root, journalPath)
	if e != nil || !s.Exists {
		return e
	}
	var j journal
	dec := json.NewDecoder(bytes.NewReader(s.Data))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&j); e != nil {
		return fmt.Errorf("invalid install journal: %w", e)
	}
	if e = dec.Decode(new(any)); e != io.EOF {
		return errors.New("invalid trailing content in install journal")
	}
	if j.SchemaVersion != 1 {
		return errors.New("unsupported install journal schema")
	}
	for _, en := range j.Entries {
		if en.Path == journalPath || en.Path == lockPath {
			return errors.New("invalid install journal target")
		}
		if _, e = safe(root, en.Path); e != nil {
			return e
		}
	}
	if !j.Committed {
		if e = rollback(root, j); e != nil {
			return e
		}
	}
	return write(root, journalPath, snapshot{})
}

// Recover rolls back an interrupted, uncommitted transaction. It refuses to
// overwrite a file changed externally since that transaction was interrupted.
func Recover(root string) error {
	a, e := rootPath(root)
	if e != nil {
		return e
	}
	unlock, e := lock(a)
	if e != nil {
		return e
	}
	defer unlock()
	return recoverLocked(a)
}

func Apply(root string, t *Transaction) error {
	a, e := rootPath(root)
	if e != nil {
		return e
	}
	if t == nil || t.root != a {
		return errors.New("installation plan belongs to a different root")
	}
	unlock, e := lock(a)
	if e != nil {
		return e
	}
	defer unlock()
	if e = recoverLocked(a); e != nil {
		return e
	}
	for _, en := range t.entries {
		s, e := read(a, en.Path)
		if e != nil {
			return e
		}
		if !equal(s, en.Before) {
			return fmt.Errorf("installation plan is stale: %s changed; create a new plan", en.Path)
		}
	}
	j := journal{SchemaVersion: 1, Entries: []entry{}}
	for _, en := range t.entries {
		if !equal(en.Before, en.After) {
			j.Entries = append(j.Entries, en)
		}
	}
	if len(j.Entries) == 0 {
		return nil
	}
	if e = write(a, journalPath, snapshot{true, encode(j), 0600}); e != nil {
		return e
	}
	for _, en := range j.Entries {
		var observed snapshot
		observed, e = read(a, en.Path)
		if e == nil && !equal(observed, en.Before) {
			e = fmt.Errorf("resource changed during installation: %s", en.Path)
		}
		if e == nil {
			e = write(a, en.Path, en.After)
		}
		if e != nil {
			if re := rollback(a, j); re != nil {
				return fmt.Errorf("installation failed: %v; recovery also failed: %w", e, re)
			}
			_ = write(a, journalPath, snapshot{})
			return e
		}
	}
	j.Committed = true
	if e = write(a, journalPath, snapshot{true, encode(j), 0600}); e != nil {
		return fmt.Errorf("installation changes written; recovery required to settle commit: %w", e)
	}
	return write(a, journalPath, snapshot{})
}
