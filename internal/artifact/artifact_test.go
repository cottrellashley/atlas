package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := Manifest{SchemaVersion: 1, ID: "@team/research", Version: "1.0.0", Kind: "skill", Skills: []string{"skills/research"}}
	b, _ := json.Marshal(m)
	if e := os.WriteFile(filepath.Join(root, ManifestName), b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(root, "skills/research"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "skills/research/SKILL.md"), []byte("---\nname: research\ndescription: Research\n---\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "skills/research/tool.sh"), []byte("#!/bin/sh\ntrue\n"), 0755); e != nil {
		t.Fatal(e)
	}
	return root
}

type archiveEntry struct {
	name string
	data []byte
	kind byte
	size int64
}

func archive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, en := range entries {
		kind := en.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(en.data))
		if en.size != 0 {
			size = en.size
		}
		if e := tw.WriteHeader(&tar.Header{Name: en.name, Mode: 0644, Size: size, Typeflag: kind}); e != nil {
			t.Fatal(e)
		}
		if len(en.data) > 0 {
			if _, e := tw.Write(en.data); e != nil {
				t.Fatal(e)
			}
		}
	}
	_ = tw.Close()
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func manifestBytes() []byte {
	b, _ := json.Marshal(Manifest{SchemaVersion: 1, ID: "@team/research", Version: "1.0.0", Kind: "bundle"})
	return b
}

func TestSnapshotDeterministicDeclaredFilesAndModes(t *testing.T) {
	root := fixture(t)
	if e := os.WriteFile(filepath.Join(root, "private.env"), []byte("SECRET=do-not-publish"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(root, "skills/research/node_modules"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "skills/research/node_modules/ignored"), []byte("ignore"), 0600); e != nil {
		t.Fatal(e)
	}
	m, a, e := Snapshot(root)
	if e != nil {
		t.Fatal(e)
	}
	_, b, e := Snapshot(root)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("snapshot bytes are not deterministic")
	}
	p, e := Extract(t.TempDir(), "file:"+root, Hash(a), a)
	if e != nil {
		t.Fatal(e)
	}
	if p.Manifest.ID != m.ID {
		t.Fatal("manifest changed")
	}
	for _, name := range []string{"private.env"} {
		if _, e = os.Stat(filepath.Join(p.Root, name)); !os.IsNotExist(e) {
			t.Fatalf("unexpected packaged file %s", name)
		}
	}
	if _, e = os.Stat(filepath.Join(p.Root, "skills/research/node_modules/ignored")); e != nil {
		t.Fatal("declared resource's vendored dependency was omitted", e)
	}
	info, e := os.Stat(filepath.Join(p.Root, "skills/research/tool.sh"))
	if e != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode not preserved: %v %v", info, e)
	}
}

func TestCacheRevalidatesArchiveAndDoesNotTrustExtractedTrees(t *testing.T) {
	home := t.TempDir()
	p, e := Local(home, fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(p.Root, "skills/research/tool.sh"), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	again, e := Cached(home, p.Source, p.Digest)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(again.Root, "skills/research/tool.sh"))
	if e != nil || bytes.Equal(b, []byte("tampered")) {
		t.Fatal("trusted mutable unpacked cache")
	}
	cache := filepath.Join(home, "packages/cache", p.Digest+".tar.gz")
	if e = os.WriteFile(cache, []byte("tampered archive"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Cached(home, p.Source, p.Digest); e == nil {
		t.Fatal("accepted corrupted archive cache")
	}
	for _, digest := range []string{"short", strings.Repeat("x", 64), strings.Repeat("../", 22)} {
		if _, e = Cached(home, p.Source, digest); e == nil {
			t.Fatal("accepted invalid digest")
		}
	}
}

func TestExtractRejectsUnsafeAndUnboundedArchives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []archiveEntry
	}{
		{"traversal", []archiveEntry{{name: "../escape", data: []byte("bad")}}},
		{"absolute", []archiveEntry{{name: "/tmp/escape", data: []byte("bad")}}},
		{"symlink", []archiveEntry{{name: "link", kind: tar.TypeSymlink}}},
		{"hardlink", []archiveEntry{{name: "link", kind: tar.TypeLink}}},
		{"duplicate", []archiveEntry{{name: "x", data: []byte("a")}, {name: "x", data: []byte("b")}}},
		{"reserved", []archiveEntry{{name: ".atlas/install-receipt.json", data: []byte("bad")}}},
		{"oversized", []archiveEntry{{name: "huge", size: MaxSize + 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := archive(t, tc.entries...)
			if _, e := Extract(t.TempDir(), "test", Hash(data), data); e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	data := archive(t, archiveEntry{name: ManifestName, data: manifestBytes()})
	if _, e := Extract(t.TempDir(), "test", strings.Repeat("0", 64), data); e == nil {
		t.Fatal("wrong digest accepted")
	}
}

func TestExtractValidatesGzipChecksum(t *testing.T) {
	data := archive(t, archiveEntry{name: ManifestName, data: manifestBytes()})
	data[len(data)-8] ^= 0xff
	if _, e := Extract(t.TempDir(), "test", Hash(data), data); e == nil {
		t.Fatal("gzip checksum corruption accepted")
	}
}

func TestSnapshotRejectsSymlinksAndSizeBeforeRead(t *testing.T) {
	root := fixture(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "skills/research/link")); e != nil {
		t.Fatal(e)
	}
	if _, _, e := Snapshot(root); e == nil {
		t.Fatal("nested symlink accepted")
	}
	if e := os.Remove(filepath.Join(root, "skills/research/link")); e != nil {
		t.Fatal(e)
	}
	f, e := os.Create(filepath.Join(root, "skills/research/large"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(MaxSize + 1); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, _, e = Snapshot(root); e == nil || !strings.Contains(e.Error(), "128 MiB") {
		t.Fatalf("expected bounded snapshot, got %v", e)
	}
}

func TestManifestStrictValidation(t *testing.T) {
	for _, input := range []string{`{"schemaVersion":99,"id":"x","version":"1.0.0","kind":"bundle"}`, `{"schemaVersion":1,"id":"x","version":"1.0.0","kind":"bundle","unknown":true}`, `{"schemaVersion":1,"id":"x","version":"1.0.0","kind":"bundle"} {}`, `null`} {
		root := t.TempDir()
		if e := os.WriteFile(filepath.Join(root, ManifestName), []byte(input), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Load(root); e == nil {
			t.Errorf("accepted manifest %s", input)
		}
	}
}

func TestCacheRefusesSymlinksAndDoesNotStoreInvalidArchives(t *testing.T) {
	home := t.TempDir()
	p, e := Local(home, fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, "packages/cache", p.Digest+".tar.gz")
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "archive")
	if e = os.WriteFile(outside, []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, path); e != nil {
		t.Fatal(e)
	}
	if _, e = Cached(home, p.Source, p.Digest); e == nil {
		t.Fatal("followed symlink archive")
	}
	invalid := archive(t, archiveEntry{name: "../escape", data: []byte("bad")})
	digest := Hash(invalid)
	if _, e = Extract(home, "bad", digest, invalid); e == nil {
		t.Fatal("accepted unsafe archive")
	}
	if _, e = os.Stat(filepath.Join(home, "packages/cache", digest+".tar.gz")); !os.IsNotExist(e) {
		t.Fatal("invalid archive persisted in cache")
	}
	other := t.TempDir()
	if e = os.Symlink(filepath.Join(home, "packages"), filepath.Join(other, "packages")); e != nil {
		t.Fatal(e)
	}
	_, data, e := Snapshot(fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Extract(other, "bad", Hash(data), data); e == nil {
		t.Fatal("followed symlink cache directory")
	}
}

func TestPackageCloseReleasesOnlyItsExtraction(t *testing.T) {
	home := t.TempDir()
	source := fixture(t)
	p, e := Local(home, source)
	if e != nil {
		t.Fatal(e)
	}
	extracted := p.Root
	p.Root = source
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(extracted); !os.IsNotExist(e) {
		t.Fatal("extraction not removed")
	}
	if _, e = os.Stat(filepath.Join(source, ManifestName)); e != nil {
		t.Fatal("cleanup followed mutable Root", e)
	}
	if _, e = os.Stat(filepath.Join(home, "packages/cache", p.Digest+".tar.gz")); e != nil {
		t.Fatal("cleanup removed immutable archive", e)
	}
	if e = (Package{Root: source}).Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(source); e != nil {
		t.Fatal("manual Package deleted source", e)
	}
}

func TestManifestRejectsIgnoredOrUnsafeResourceFields(t *testing.T) {
	base := Manifest{SchemaVersion: 1, ID: "@team/research", Version: "1.0.0", Kind: "bundle"}
	cases := []Manifest{}
	m := base
	m.Config = map[string]string{"cluade": "settings.json"}
	cases = append(cases, m)
	m = base
	m.Files = map[string]map[string]string{"unknown": {"file": "source"}}
	cases = append(cases, m)
	m = base
	m.Native = &Native{Path: "native"}
	cases = append(cases, m)
	m = base
	m.Kind = "project-template"
	m.Template = "../../outside"
	cases = append(cases, m)
	m = base
	m.Kind = "project-template"
	m.Template = "/outside"
	cases = append(cases, m)
	m = base
	m.Config = map[string]string{"claude": "../outside"}
	cases = append(cases, m)
	m = base
	m.Kind = "claude-plugin"
	m.Native = &Native{Path: "plugin", Target: "server"}
	cases = append(cases, m)
	for i, m := range cases {
		if e := Validate(m); e == nil {
			t.Errorf("manifest case %d accepted", i)
		}
	}
	root := t.TempDir()
	m = base
	m.Kind = "project-template"
	m.Template = "template"
	b, _ := json.Marshal(m)
	if e := os.WriteFile(filepath.Join(root, ManifestName), b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "template"), []byte("not a directory"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e := Snapshot(root); e == nil {
		t.Fatal("file accepted as template directory")
	}
}
