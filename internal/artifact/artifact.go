package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/project"
)

const ManifestName = "atlas-package.json"
const MaxSize = 128 << 20

var idPattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9.-]*/)?[a-z0-9][a-z0-9._-]*$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)

func Validate(m Manifest) error {
	if m.SchemaVersion != 1 || !idPattern.MatchString(m.ID) || !versionPattern.MatchString(m.Version) {
		return errors.New("package requires schemaVersion 1, a lowercase namespaced id, and a semantic version (e.g. 1.0.0)")
	}
	if parts := strings.SplitN(m.Version, "-", 2); len(parts) == 2 {
		for _, part := range strings.Split(parts[1], ".") {
			if part == "" {
				return errors.New("semantic version has an empty prerelease identifier")
			}
			onlyDigits := true
			for _, c := range part {
				if c < '0' || c > '9' {
					onlyDigits = false
					break
				}
			}
			if onlyDigits && len(part) > 1 && part[0] == '0' {
				return errors.New("numeric prerelease identifier has a leading zero")
			}
		}
	}
	switch m.Kind {
	case "skill", "mcp", "harness-config", "bundle", "claude-plugin", "pi-package", "opencode-plugin", "project-template":
	default:
		return fmt.Errorf("unsupported package kind %q", m.Kind)
	}
	for _, h := range m.Harnesses {
		if h != "claude" && h != "pi" && h != "omp" && h != "opencode" {
			return fmt.Errorf("unknown harness %q", h)
		}
	}
	validProvider := func(h string) bool { return h == "*" || h == "claude" || h == "pi" || h == "omp" || h == "opencode" }
	for h, path := range m.Config {
		if !validProvider(h) {
			return fmt.Errorf("unknown config harness %q", h)
		}
		if e := validatePath(path); e != nil {
			return e
		}
	}
	for h, files := range m.Files {
		if !validProvider(h) {
			return fmt.Errorf("unknown file harness %q", h)
		}
		for target, source := range files {
			if e := validatePath(target); e != nil {
				return e
			}
			if e := validatePath(source); e != nil {
				return e
			}
		}
	}
	for _, path := range m.Skills {
		if e := validatePath(path); e != nil {
			return e
		}
	}
	if m.MCP != "" {
		if e := validatePath(m.MCP); e != nil {
			return e
		}
	}
	if m.Template != "" {
		if m.Kind != "project-template" {
			return errors.New("template applies only to project-template packages")
		}
		if e := validatePath(m.Template); e != nil {
			return e
		}
	}
	for _, d := range m.Dependencies {
		if d.Source == "" {
			return errors.New("empty dependency source")
		}
		for _, h := range d.Harnesses {
			if h != "claude" && h != "pi" && h != "omp" && h != "opencode" {
				return fmt.Errorf("unknown dependency harness %q", h)
			}
		}
	}
	if m.Kind == "skill" && len(m.Skills) == 0 {
		return errors.New("skill package requires skills paths")
	}
	if m.Kind == "mcp" && m.MCP == "" {
		return errors.New("mcp package requires an mcp file")
	}
	if strings.HasSuffix(m.Kind, "-plugin") || m.Kind == "pi-package" {
		if m.Native == nil || (m.Native.Path == "") == (m.Native.Source == "") {
			return errors.New("native package requires exactly one native.path or native.source")
		}
		if m.Native.Path != "" {
			if e := validatePath(m.Native.Path); e != nil {
				return e
			}
		}
		if m.Native.Target != "" && m.Kind != "opencode-plugin" {
			return errors.New("native.target applies only to OpenCode plugins")
		}
		if m.Native.Target != "" && m.Native.Target != "server" && m.Native.Target != "tui" && m.Native.Target != "both" {
			return errors.New("unknown OpenCode plugin target")
		}
		if m.Native.Marketplace != "" && m.Kind != "claude-plugin" {
			return errors.New("native.marketplace applies only to Claude plugins")
		}
	} else if m.Native != nil {
		return errors.New("native resources require a claude-plugin, pi-package, or opencode-plugin package kind")
	}
	if m.Kind == "project-template" && m.Template == "" {
		return errors.New("project-template requires template directory")
	}
	return nil
}

func validatePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return fmt.Errorf("unsafe package path %q", path)
	}
	clean := filepath.Clean(path)
	if clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe package path %q", path)
	}
	return nil
}

func validateTemplateDirectory(root string, m Manifest) error {
	if m.Template == "" {
		return nil
	}
	path := filepath.Join(root, m.Template)
	st, e := os.Stat(path)
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return errors.New("package template must be a directory")
	}
	for cur := path; cur != root; cur = filepath.Dir(cur) {
		st, e := os.Lstat(cur)
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("package template must not contain a symlink path")
		}
	}
	return nil
}

func Load(root string) (Manifest, error) {
	var m Manifest
	b, e := os.ReadFile(filepath.Join(root, ManifestName))
	if e != nil {
		return m, e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&m); e != nil {
		return m, fmt.Errorf("%s: %w", ManifestName, e)
	}
	if dec.Decode(new(any)) != io.EOF {
		return m, errors.New("unexpected data after package manifest")
	}
	return m, Validate(m)
}

func Hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

// Snapshot includes only declared resource trees and conventional documentation.
// It never publishes the caller's entire working directory implicitly.
func Snapshot(root string) (Manifest, []byte, error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return Manifest{}, nil, e
	}
	m, e := Load(root)
	if e != nil {
		return m, nil, e
	}
	if e = validateTemplateDirectory(root, m); e != nil {
		return m, nil, e
	}
	paths := []string{ManifestName}
	paths = append(paths, m.Skills...)
	if m.MCP != "" {
		paths = append(paths, m.MCP)
	}
	for _, p := range m.Config {
		paths = append(paths, p)
	}
	for _, entries := range m.Files {
		for _, p := range entries {
			paths = append(paths, p)
		}
	}
	if m.Native != nil && m.Native.Path != "" {
		paths = append(paths, m.Native.Path)
	}
	if m.Template != "" {
		paths = append(paths, m.Template)
	}
	for _, p := range []string{"README.md", "LICENSE", "LICENSE.md"} {
		if _, e := os.Stat(filepath.Join(root, p)); e == nil {
			paths = append(paths, p)
		}
	}
	entries := map[string]File{}
	var total int64
	for _, p := range paths {
		if p == "" || strings.Contains(p, "\\") || filepath.IsAbs(p) || filepath.Clean(p) == ".." || strings.HasPrefix(filepath.Clean(p), ".."+string(filepath.Separator)) {
			return m, nil, fmt.Errorf("unsafe package source %q", p)
		}
		full := filepath.Join(root, p)
		// Reject symlinks in every parent, not just WalkDir's final entry.
		for cur := full; cur != root; cur = filepath.Dir(cur) {
			st, err := os.Lstat(cur)
			if err != nil {
				return m, nil, err
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return m, nil, fmt.Errorf("package symlink %s", cur)
			}
		}
		e = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".atlas":
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("package entry must be a regular file: %s", rel)
			}
			if _, ok := entries[rel]; ok {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > MaxSize {
				return errors.New("package exceeds 128 MiB")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entries[rel] = File{Path: filepath.ToSlash(rel), Data: b, Mode: info.Mode().Perm()}
			return nil
		})
		if e != nil {
			return m, nil, e
		}
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, key := range keys {
		f := entries[key]
		h := &tar.Header{Name: f.Path, Mode: int64(f.Mode), Size: int64(len(f.Data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if e = tw.WriteHeader(h); e != nil {
			return m, nil, e
		}
		if _, e = tw.Write(f.Data); e != nil {
			return m, nil, e
		}
	}
	if e = tw.Close(); e != nil {
		return m, nil, e
	}
	if e = gz.Close(); e != nil {
		return m, nil, e
	}
	return m, out.Bytes(), nil
}

// Extract validates the digest before extracting into a private new directory.
// The cache key is the archive digest; cached bytes are reverified on every use.
func Extract(home, source, digest string, data []byte) (Package, error) {
	var p Package
	if len(data) > MaxSize || Hash(data) != digest {
		return p, errors.New("package archive digest mismatch or size limit exceeded")
	}
	cache := filepath.Join(home, "packages", "cache")
	if e := safeCacheDir(cache); e != nil {
		return p, e
	}
	dir, e := os.MkdirTemp(cache, "unpack-")
	if e != nil {
		return p, e
	}
	defer os.RemoveAll(dir)
	gz, e := gzip.NewReader(bytes.NewReader(data))
	if e != nil {
		return p, e
	}
	defer gz.Close()
	// Bound all decompressed bytes, including tar headers and trailing padding.
	// A content-only bound would miss archives with excessive empty entries.
	limited := &io.LimitedReader{R: gz, N: MaxSize + (16 << 20) + 1}
	tr := tar.NewReader(limited)
	var total int64
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return p, e
		}
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if h.Typeflag != tar.TypeReg || clean != h.Name || strings.Contains(h.Name, "\\") || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || seen[clean] {
			return p, fmt.Errorf("unsafe or duplicate archive entry %q", h.Name)
		}
		if clean == ".atlas" || strings.HasPrefix(clean, ".atlas/") || clean == ".git" || strings.HasPrefix(clean, ".git/") {
			return p, fmt.Errorf("reserved archive entry %q", h.Name)
		}
		if h.Size < 0 || h.Size > MaxSize-total {
			return p, errors.New("unpacked package exceeds 128 MiB")
		}
		total += h.Size
		if len(seen) >= 10000 {
			return p, errors.New("package has too many archive entries")
		}
		seen[clean] = true
		path := filepath.Join(dir, clean)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return p, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(h.Mode)&0777)
		if e != nil {
			return p, e
		}
		_, e = io.Copy(f, tr)
		ce := f.Close()
		if e != nil {
			return p, e
		}
		if ce != nil {
			return p, ce
		}
	}
	// tar stops at its end marker before gzip necessarily verifies the CRC.
	if _, e = io.Copy(io.Discard, limited); e != nil {
		return p, fmt.Errorf("invalid package compression: %w", e)
	}
	if limited.N == 0 {
		return p, errors.New("unpacked archive exceeds size limit")
	}
	m, e := Load(dir)
	if e != nil {
		return p, e
	}
	if e = validateTemplateDirectory(dir, m); e != nil {
		return p, e
	}
	archivePath := filepath.Join(cache, digest+".tar.gz")
	if e = noSymlink(archivePath); e != nil {
		return p, e
	}
	if e = project.Atomic(archivePath, data, 0600); e != nil {
		return p, e
	}
	// Unique extraction directories avoid trusting writable cached trees and allow
	// concurrent readers. Callers render files from this verified snapshot.
	dest := filepath.Join(cache, digest+"-"+filepath.Base(dir))
	if e = os.Rename(dir, dest); e != nil {
		return p, e
	}
	return Package{Manifest: m, Source: source, Digest: digest, Root: dest, cleanupRoot: dest}, nil
}

func Local(home, path string) (Package, error) {
	m, data, e := Snapshot(path)
	if e != nil {
		return Package{}, e
	}
	_ = m
	abs, e := filepath.Abs(path)
	if e != nil {
		return Package{}, e
	}
	return Extract(home, "file:"+abs, Hash(data), data)
}

func Cached(home, source, digest string) (Package, error) {
	if len(digest) != 64 {
		return Package{}, errors.New("invalid package digest")
	}
	if _, e := hex.DecodeString(digest); e != nil {
		return Package{}, errors.New("invalid package digest")
	}
	path := filepath.Join(home, "packages", "cache", digest+".tar.gz")
	if e := noSymlink(path); e != nil {
		return Package{}, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return Package{}, e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSize {
		return Package{}, errors.New("invalid cached archive file")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return Package{}, e
	}
	return Extract(home, source, digest, data)
}

func noSymlink(path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	for p := abs; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in package cache: %s", p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func safeCacheDir(path string) error {
	if e := noSymlink(path); e != nil {
		return e
	}
	return os.MkdirAll(path, 0700)
}
