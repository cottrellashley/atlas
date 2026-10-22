// Package resolve resolves package graphs and locks immutable release identities.
package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/catalog"
)

type Request struct {
	Source    string `json:"source"`
	Harness   string `json:"harness"`
	HarnessID string `json:"harnessId,omitempty"`
	ID        string `json:"id,omitempty"`
}
type Declaration struct {
	Scope         string    `json:"scope,omitempty"`
	SchemaVersion int       `json:"schemaVersion"`
	Packages      []Request `json:"packages"`
}
type Locked struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Kind         string   `json:"kind"`
	Harness      string   `json:"harness"`
	Source       string   `json:"source"`
	Digest       string   `json:"sha256"`
	Dependencies []string `json:"dependencies,omitempty"`
}
type Lock struct {
	Scope         string    `json:"scope,omitempty"`
	SchemaVersion int       `json:"schemaVersion"`
	Packages      []Locked  `json:"packages"`
	Roots         []Request `json:"roots"`
}
type Resolver struct {
	Home     string
	Catalogs catalog.Settings
	Catalog  string
}

func Key(harness, id string) string { return harness + ":" + id }
func Read(root string) (Declaration, Lock, error) {
	d := Declaration{SchemaVersion: 1, Packages: []Request{}}
	l := Lock{SchemaVersion: 1, Packages: []Locked{}, Roots: []Request{}}
	for name, target := range map[string]any{"packages.json": &d, "packages.lock.json": &l} {
		b, e := os.ReadFile(filepath.Join(root, ".atlas", name))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return d, l, e
		}
		if e = json.Unmarshal(b, target); e != nil {
			return d, l, e
		}
	}
	if d.SchemaVersion != 1 || l.SchemaVersion != 1 {
		return d, l, errors.New("unsupported package declaration/lock schema")
	}
	return d, l, nil
}
func Marshal(v any) []byte { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
func SplitID(s string) (id, version string) {
	idx := strings.LastIndex(s, "@")
	if idx > 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}
func (r Resolver) Fetch(ctx context.Context, source string) (artifact.Package, error) {
	if strings.HasPrefix(source, "file:") {
		return artifact.Local(r.Home, strings.TrimPrefix(source, "file:"))
	}
	if st, e := os.Stat(source); e == nil && st.IsDir() {
		return artifact.Local(r.Home, source)
	}
	catName := r.Catalog
	if strings.HasPrefix(source, "github:") {
		// github:HOST/OWNER/REPO#PACKAGE@VERSION is independent of catalog aliases.
		raw := strings.TrimPrefix(source, "github:")
		parts := strings.SplitN(raw, "#", 2)
		if len(parts) != 2 {
			return artifact.Package{}, errors.New("GitHub package source must be github:HOST/OWNER/REPO#ID@VERSION")
		}
		repo := strings.Split(parts[0], "/")
		if len(repo) != 3 {
			return artifact.Package{}, errors.New("invalid GitHub package source")
		}
		cfg := catalog.Config{Host: repo[0], Repository: repo[1] + "/" + repo[2]}
		for _, v := range r.Catalogs.Catalogs {
			if v.Host == cfg.Host && v.Repository == cfg.Repository {
				cfg = v
				break
			}
		}
		return r.fetchRemote(ctx, cfg, parts[1])
	}
	if idx := strings.Index(source, "::"); idx >= 0 {
		catName = source[:idx]
		source = source[idx+2:]
	}
	_, cfg, e := r.Catalogs.Resolve(catName)
	if e != nil {
		return artifact.Package{}, e
	}
	return r.fetchRemote(ctx, cfg, source)
}
func (r Resolver) fetchRemote(ctx context.Context, cfg catalog.Config, spec string) (artifact.Package, error) {
	c, e := catalog.New(ctx, r.Home, cfg)
	if e != nil {
		return artifact.Package{}, e
	}
	id, version := SplitID(spec)
	var entry catalog.Entry
	if version == "" || version == "latest" {
		entry, e = c.Latest(ctx, id)
	} else {
		entry, e = c.Get(ctx, id, version)
	}
	if e != nil {
		return artifact.Package{}, e
	}
	source := "github:" + cfg.Host + "/" + cfg.Repository + "#" + entry.Manifest.ID + "@" + entry.Manifest.Version
	if p, e := artifact.Cached(r.Home, source, entry.Digest); e == nil {
		if !sameManifest(p.Manifest, entry.Manifest) {
			_ = p.Close()
			return artifact.Package{}, errors.New("cached package manifest disagrees with release metadata")
		}
		return p, nil
	}
	data, e := c.Download(ctx, entry)
	if e != nil {
		return artifact.Package{}, e
	}
	p, e := artifact.Extract(r.Home, source, entry.Digest, data)
	if e == nil && !sameManifest(p.Manifest, entry.Manifest) {
		_ = p.Close()
		return artifact.Package{}, errors.New("archive manifest disagrees with release metadata")
	}
	return p, e
}
func (r Resolver) LockedPackage(ctx context.Context, l Locked) (artifact.Package, error) {
	if p, e := artifact.Cached(r.Home, l.Source, l.Digest); e == nil {
		if p.Manifest.ID != l.ID || p.Manifest.Version != l.Version || p.Manifest.Kind != l.Kind {
			_ = p.Close()
			return artifact.Package{}, errors.New("lock package identity mismatch")
		}
		return p, nil
	}
	p, e := r.Fetch(ctx, l.Source)
	if e != nil {
		return p, e
	}
	if p.Digest != l.Digest || p.Manifest.ID != l.ID || p.Manifest.Version != l.Version || p.Manifest.Kind != l.Kind {
		_ = p.Close()
		return artifact.Package{}, fmt.Errorf("locked package changed: %s; expected %s", l.ID, l.Digest)
	}
	return p, nil
}
func supports(h string, hs []string) bool {
	if len(hs) == 0 {
		return true
	}
	for _, v := range hs {
		if v == h {
			return true
		}
	}
	return false
}
func dependencySource(parent artifact.Package, source string) string {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		if strings.HasPrefix(parent.Source, "file:") {
			return "file:" + filepath.Join(strings.TrimPrefix(parent.Source, "file:"), source)
		}
	}
	// Unqualified registry dependencies inherit their parent's catalog, avoiding
	// accidental resolution against another team's configured default.
	if strings.HasPrefix(parent.Source, "github:") && !strings.Contains(source, "::") && !strings.HasPrefix(source, "github:") && !strings.HasPrefix(source, "file:") {
		prefix := strings.SplitN(parent.Source, "#", 2)[0]
		return prefix + "#" + source
	}
	return source
}

// Resolve preserves previous locks unless their root is explicitly updated.
// Dependencies of a selected release are always resolved as that release declares.
func (r Resolver) Resolve(ctx context.Context, requests []Request, old Lock, update map[string]bool) (Lock, []artifact.Package, error) {
	lock := Lock{SchemaVersion: 1, Roots: []Request{}, Packages: []Locked{}}
	pkgs := []artifact.Package{}
	complete := false
	defer func() {
		if !complete {
			for _, p := range pkgs {
				_ = p.Close()
			}
		}
	}()
	seen := map[string]Locked{}
	visiting := map[string]bool{}
	oldByKey := map[string]Locked{}
	if old.SchemaVersion != 0 && old.SchemaVersion != 1 {
		return lock, nil, errors.New("unsupported package lock schema")
	}
	for _, v := range old.Packages {
		key := Key(v.Harness, v.ID)
		if _, exists := oldByKey[key]; exists {
			return lock, nil, fmt.Errorf("duplicate package in lock: %s", key)
		}
		oldByKey[key] = v
	}
	accept := func(p artifact.Package, item Locked) (Locked, error) {
		key := Key(item.Harness, item.ID)
		if prev, ok := seen[key]; ok {
			if prev.Digest != item.Digest {
				return item, fmt.Errorf("conflicting versions of %s", key)
			}
			return prev, nil
		}
		seen[key] = item
		lock.Packages = append(lock.Packages, item)
		pkgs = append(pkgs, p)
		return item, nil
	}
	var visitLocked func(Locked) (Locked, error)
	visitLocked = func(item Locked) (Locked, error) {
		key := Key(item.Harness, item.ID)
		if visiting[key] {
			return item, fmt.Errorf("dependency cycle at %s", key)
		}
		if prev, ok := seen[key]; ok {
			if prev.Digest != item.Digest {
				return item, fmt.Errorf("conflicting versions of %s", key)
			}
			return prev, nil
		}
		visiting[key] = true
		defer delete(visiting, key)
		p, e := r.LockedPackage(ctx, item)
		if e != nil {
			return item, e
		}
		retained := false
		defer func() {
			if !retained {
				_ = p.Close()
			}
		}()
		if !supports(item.Harness, p.Manifest.Harnesses) {
			return item, fmt.Errorf("%s does not support %s", item.ID, item.Harness)
		}
		if e = r.validateLockedDependencies(p, item, oldByKey); e != nil {
			return item, e
		}
		for _, dep := range item.Dependencies {
			v, ok := oldByKey[dep]
			if !ok {
				return item, errors.New("incomplete dependency lock")
			}
			if _, e = visitLocked(v); e != nil {
				return item, e
			}
		}
		accepted, e := accept(p, item)
		retained = e == nil
		return accepted, e
	}
	var visit func(string, string) (Locked, error)
	visit = func(source, h string) (Locked, error) {
		p, e := r.Fetch(ctx, source)
		if e != nil {
			return Locked{}, e
		}
		retained := false
		defer func() {
			if !retained {
				_ = p.Close()
			}
		}()
		if !supports(h, p.Manifest.Harnesses) {
			return Locked{}, fmt.Errorf("%s does not support %s", p.Manifest.ID, h)
		}
		key := Key(h, p.Manifest.ID)
		if visiting[key] {
			return Locked{}, fmt.Errorf("package dependency cycle at %s", key)
		}
		if prev, ok := seen[key]; ok {
			if prev.Digest != p.Digest {
				return Locked{}, fmt.Errorf("conflicting versions of %s", key)
			}
			return prev, nil
		}
		visiting[key] = true
		defer delete(visiting, key)
		item := Locked{ID: p.Manifest.ID, Version: p.Manifest.Version, Kind: p.Manifest.Kind, Harness: h, Source: p.Source, Digest: p.Digest}
		for _, dep := range p.Manifest.Dependencies {
			if !supports(h, dep.Harnesses) {
				continue
			}
			v, e := visit(dependencySource(p, dep.Source), h)
			if e != nil {
				return item, e
			}
			item.Dependencies = append(item.Dependencies, Key(h, v.ID))
		}
		accepted, e := accept(p, item)
		retained = e == nil
		return accepted, e
	}
	for _, request := range requests {
		if request.Harness != "claude" && request.Harness != "pi" && request.Harness != "omp" && request.Harness != "opencode" {
			return lock, nil, fmt.Errorf("unsupported package harness %q", request.Harness)
		}
		var item Locked
		var e error
		var previous *Locked
		if !update[request.Source] {
			for _, root := range old.Roots {
				if root.Source == request.Source && root.Harness == request.Harness {
					v, ok := oldByKey[Key(root.Harness, root.ID)]
					if !ok {
						return lock, nil, errors.New("lock root has no package")
					}
					previous = &v
					break
				}
			}
		}
		if previous != nil {
			if !r.sourceMatches(request.Source, *previous) {
				return lock, nil, fmt.Errorf("locked package source disagrees with requested root %s", request.Source)
			}
			item, e = visitLocked(*previous)
		} else {
			item, e = visit(request.Source, request.Harness)
		}
		if e != nil {
			return lock, nil, e
		}
		request.ID = item.ID
		lock.Roots = append(lock.Roots, request)
	}
	complete = true
	return lock, pkgs, nil
}

func sameManifest(a, b artifact.Manifest) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// sourceMatches validates a lock against source intent without resolving mutable
// latest references or requiring network access. Exact GitHub sources also bind
// repository identity. A catalog alias can be absent on a new machine because
// locked entries already carry its canonical GitHub source.
func (r Resolver) sourceMatches(source string, item Locked) bool {
	if strings.HasPrefix(source, "file:") || strings.HasPrefix(item.Source, "file:") {
		if !strings.HasPrefix(item.Source, "file:") {
			return false
		}
		path := strings.TrimPrefix(source, "file:")
		abs, e := filepath.Abs(path)
		return e == nil && "file:"+abs == item.Source
	}
	prefix := ""
	if strings.HasPrefix(source, "github:") {
		parts := strings.SplitN(source, "#", 2)
		if len(parts) != 2 {
			return false
		}
		prefix = parts[0] + "#"
		source = parts[1]
	} else if i := strings.Index(source, "::"); i >= 0 {
		alias := source[:i]
		source = source[i+2:]
		if cfg, ok := r.Catalogs.Catalogs[alias]; ok {
			host := cfg.Host
			if host == "" {
				host = "github.com"
			}
			prefix = "github:" + host + "/" + cfg.Repository + "#"
		}
	}
	id, version := SplitID(source)
	if id != item.ID || version != "" && version != "latest" && version != item.Version {
		return false
	}
	if !strings.HasPrefix(item.Source, "github:") {
		return false
	}
	actual := strings.SplitN(item.Source, "#", 2)
	if len(actual) != 2 || actual[1] != item.ID+"@"+item.Version {
		return false
	}
	return prefix == "" || actual[0]+"#" == prefix
}

func (r Resolver) validateLockedDependencies(p artifact.Package, item Locked, all map[string]Locked) error {
	expected := []string{}
	for _, dep := range p.Manifest.Dependencies {
		if supports(item.Harness, dep.Harnesses) {
			expected = append(expected, dependencySource(p, dep.Source))
		}
	}
	used := map[string]bool{}
	for _, source := range expected {
		matched := ""
		for _, key := range item.Dependencies {
			child, ok := all[key]
			if ok && child.Harness == item.Harness && r.sourceMatches(source, child) {
				matched = key
				break
			}
		}
		if matched == "" {
			return fmt.Errorf("dependency lock for %s omits or changes required source %s", item.ID, source)
		}
		used[matched] = true
	}
	for _, key := range item.Dependencies {
		if !used[key] {
			return fmt.Errorf("dependency lock for %s contains undeclared dependency %s", item.ID, key)
		}
	}
	return nil
}
func Matches(d Declaration, l Lock) bool {
	if effectiveScope(d.Scope) != effectiveScope(l.Scope) {
		return false
	}
	if len(d.Packages) != len(l.Roots) {
		return false
	}
	for i, v := range d.Packages {
		if v.Source != l.Roots[i].Source || v.Harness != l.Roots[i].Harness || v.HarnessID != l.Roots[i].HarnessID {
			return false
		}
	}
	return true
}

func effectiveScope(s string) string {
	if s == "" {
		return "project"
	}
	return s
}
