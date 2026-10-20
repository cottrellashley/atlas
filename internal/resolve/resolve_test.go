package resolve

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/catalog"
	"github.com/google/go-github/v82/github"
)

func packageDir(t *testing.T, root, name string, deps ...artifact.Dependency) string {
	t.Helper()
	path := filepath.Join(root, name)
	if e := os.MkdirAll(path, 0700); e != nil {
		t.Fatal(e)
	}
	writeManifest(t, path, artifact.Manifest{SchemaVersion: 1, ID: "@team/" + name, Version: "1.0.0", Kind: "bundle", Dependencies: deps})
	return path
}
func writeManifest(t *testing.T, path string, m artifact.Manifest) {
	t.Helper()
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(path, artifact.ManifestName), b, 0600); e != nil {
		t.Fatal(e)
	}
}
func copyLock(t *testing.T, l Lock) Lock {
	t.Helper()
	b, e := json.Marshal(l)
	if e != nil {
		t.Fatal(e)
	}
	var out Lock
	if e = json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestSharedDependencyGraphAndHarnessSelection(t *testing.T) {
	root := t.TempDir()
	shared := packageDir(t, root, "shared")
	_ = shared
	a := packageDir(t, root, "a", artifact.Dependency{Source: "../shared"})
	b := packageDir(t, root, "b", artifact.Dependency{Source: "../shared"}, artifact.Dependency{Source: "../missing-pi-only", Harnesses: []string{"pi"}})
	r := Resolver{Home: t.TempDir()}
	requests := []Request{{Source: a, Harness: "claude", HarnessID: "company-claude"}, {Source: b, Harness: "claude", HarnessID: "company-claude"}}
	lock, pkgs, e := r.Resolve(context.Background(), requests, Lock{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(lock.Packages) != 3 || len(pkgs) != 3 || len(lock.Roots) != 2 {
		t.Fatalf("bad graph %#v", lock)
	}
	if lock.Packages[0].ID != "@team/shared" {
		t.Fatal("dependencies are not before dependents")
	}
	for _, item := range lock.Packages[1:] {
		if len(item.Dependencies) != 1 || item.Dependencies[0] != "claude:@team/shared" {
			t.Fatalf("shared dependency missing %#v", item)
		}
	}
	if lock.Roots[0].HarnessID != "company-claude" {
		t.Fatal("configured harness alias lost")
	}
	again, _, e := r.Resolve(context.Background(), lock.Roots, lock, nil)
	if e != nil {
		t.Fatal(e)
	}
	if string(Marshal(lock)) != string(Marshal(again)) {
		t.Fatal("locked graph is not reproducible")
	}
}

func TestLockedLocalPackageUsesSnapshotUntilExplicitUpdate(t *testing.T) {
	path := packageDir(t, t.TempDir(), "research")
	r := Resolver{Home: t.TempDir()}
	requests := []Request{{Source: path, Harness: "pi"}}
	lock, _, e := r.Resolve(context.Background(), requests, Lock{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	m, e := artifact.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	m.Version = "1.1.0"
	writeManifest(t, path, m)
	same, _, e := r.Resolve(context.Background(), requests, lock, nil)
	if e != nil {
		t.Fatal(e)
	}
	if same.Packages[0].Version != "1.0.0" || same.Packages[0].Digest != lock.Packages[0].Digest {
		t.Fatal("local edit changed locked installation")
	}
	updated, _, e := r.Resolve(context.Background(), requests, lock, map[string]bool{path: true})
	if e != nil {
		t.Fatal(e)
	}
	if updated.Packages[0].Version != "1.1.0" || updated.Packages[0].Digest == lock.Packages[0].Digest {
		t.Fatal("explicit update did not acquire edited package")
	}
	if e = os.Remove(filepath.Join(r.Home, "packages/cache", lock.Packages[0].Digest+".tar.gz")); e != nil {
		t.Fatal(e)
	}
	if _, e = r.LockedPackage(context.Background(), lock.Packages[0]); e == nil {
		t.Fatal("missing old cache silently replaced with changed local contents")
	}
}

func TestGraphRejectsCyclesAndConflictingVersions(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		a := packageDir(t, root, "a", artifact.Dependency{Source: "../b"})
		packageDir(t, root, "b", artifact.Dependency{Source: "../a"})
		_, _, e := (Resolver{Home: t.TempDir()}).Resolve(context.Background(), []Request{{Source: a, Harness: "claude"}}, Lock{}, nil)
		if e == nil || !strings.Contains(e.Error(), "cycle") {
			t.Fatalf("expected cycle error, got %v", e)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		root := t.TempDir()
		a := packageDir(t, root, "a")
		b := packageDir(t, root, "b")
		writeManifest(t, b, artifact.Manifest{SchemaVersion: 1, ID: "@team/a", Version: "2.0.0", Kind: "bundle"})
		_, _, e := (Resolver{Home: t.TempDir()}).Resolve(context.Background(), []Request{{Source: a, Harness: "claude"}, {Source: b, Harness: "claude"}}, Lock{}, nil)
		if e == nil || !strings.Contains(e.Error(), "conflicting") {
			t.Fatalf("expected version conflict, got %v", e)
		}
	})
}

func TestLockedGraphRejectsManifestDisagreement(t *testing.T) {
	root := t.TempDir()
	a := packageDir(t, root, "a", artifact.Dependency{Source: "../b"})
	packageDir(t, root, "b")
	r := Resolver{Home: t.TempDir()}
	requests := []Request{{Source: a, Harness: "claude"}}
	lock, _, e := r.Resolve(context.Background(), requests, Lock{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Lock)
	}{
		{"kind", func(l *Lock) { l.Packages[1].Kind = "skill" }},
		{"omitted-dependency", func(l *Lock) { l.Packages[1].Dependencies = nil }},
		{"missing-dependency", func(l *Lock) { l.Packages = l.Packages[1:] }},
		{"wrong-harness-dependency", func(l *Lock) { l.Packages[0].Harness = "pi"; l.Packages[1].Dependencies = []string{"pi:@team/b"} }},
		{"wrong-source-dependency", func(l *Lock) { l.Packages[0].Source = "file:/different/b" }},
		{"wrong-root-source", func(l *Lock) { l.Packages[1].Source = "file:/different/a" }},
		{"duplicate-key", func(l *Lock) { l.Packages = append(l.Packages, l.Packages[0]) }},
		{"wrong-schema", func(l *Lock) { l.SchemaVersion = 99 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := copyLock(t, lock)
			tc.mutate(&bad)
			if _, _, e = r.Resolve(context.Background(), requests, bad, nil); e == nil {
				t.Fatal("accepted changed lock semantics")
			}
		})
	}
}

func TestHarnessCompatibilityAndDeclarationMatch(t *testing.T) {
	path := packageDir(t, t.TempDir(), "claude-only")
	writeManifest(t, path, artifact.Manifest{SchemaVersion: 1, ID: "@team/claude-only", Version: "1.0.0", Kind: "bundle", Harnesses: []string{"claude"}})
	r := Resolver{Home: t.TempDir()}
	for _, h := range []string{"pi", "unknown"} {
		if _, _, e := r.Resolve(context.Background(), []Request{{Source: path, Harness: h}}, Lock{}, nil); e == nil {
			t.Fatalf("accepted unsupported harness %s", h)
		}
	}
	d := Declaration{SchemaVersion: 1, Packages: []Request{{Source: path, Harness: "claude", HarnessID: "company"}}}
	l := Lock{SchemaVersion: 1, Roots: append([]Request(nil), d.Packages...)}
	if !Matches(d, l) {
		t.Fatal("equivalent declarations differ")
	}
	l.Roots[0].HarnessID = "other"
	if Matches(d, l) {
		t.Fatal("different harness aliases match")
	}
}

func TestCanonicalSourceMatching(t *testing.T) {
	r := Resolver{Catalogs: catalog.Settings{Catalogs: map[string]catalog.Config{"company": {Host: "github.company.example", Repository: "team/catalog"}}}}
	item := Locked{ID: "@team/research", Version: "1.0.0", Source: "github:github.company.example/team/catalog#@team/research@1.0.0"}
	for _, source := range []string{"@team/research", "@team/research@latest", "@team/research@1.0.0", "company::@team/research@1.0.0", item.Source} {
		if !r.sourceMatches(source, item) {
			t.Errorf("matching source rejected %s", source)
		}
	}
	for _, source := range []string{"@team/research@2.0.0", "@other/research", "github:github.company.example/other/catalog#@team/research@1.0.0"} {
		if r.sourceMatches(source, item) {
			t.Errorf("different source accepted %s", source)
		}
	}
}

func TestRemoteArchiveManifestMustMatchCatalogMetadata(t *testing.T) {
	path := packageDir(t, t.TempDir(), "research")
	m, data, e := artifact.Snapshot(path)
	if e != nil {
		t.Fatal(e)
	}
	advertised := m
	advertised.Description = "different catalog content"
	releaseMetadata := catalog.Release{SchemaVersion: 1, Manifest: advertised, Digest: artifact.Hash(data)}
	body, _ := json.Marshal(releaseMetadata)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/team/catalog/releases/tags/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&github.RepositoryRelease{TagName: github.Ptr(catalog.Tag(m.ID, m.Version)), Body: github.Ptr("Atlas package release v1\n\n```json\n" + string(body) + "\n```\n"), Assets: []*github.ReleaseAsset{{ID: github.Ptr(int64(20)), Name: github.Ptr("package.tar.gz")}}})
		case r.URL.Path == "/repos/team/catalog/releases/assets/20":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
		default:
			http.Error(w, fmt.Sprintf("unexpected %s", r.URL.Path), 500)
		}
	}))
	defer server.Close()
	previous := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	t.Setenv("ATLAS_TEST_TOKEN", "test-token")
	r := Resolver{Home: t.TempDir(), Catalogs: catalog.Settings{Default: "company", Catalogs: map[string]catalog.Config{"company": {Host: "github.company.example", Repository: "team/catalog", TokenEnv: "ATLAS_TEST_TOKEN", APIURL: server.URL}}}}
	if _, e = r.Fetch(context.Background(), m.ID+"@"+m.Version); e == nil || !strings.Contains(e.Error(), "manifest disagrees") {
		t.Fatalf("accepted misleading catalog manifest: %v", e)
	}
}

func TestResolverCleansDiscardedAndFailedAcquisitions(t *testing.T) {
	count := func(t *testing.T, home string) int {
		t.Helper()
		dirs, e := filepath.Glob(filepath.Join(home, "packages/cache", "*-unpack-*"))
		if e != nil {
			t.Fatal(e)
		}
		return len(dirs)
	}
	t.Run("shared", func(t *testing.T) {
		root := t.TempDir()
		packageDir(t, root, "shared")
		a := packageDir(t, root, "a", artifact.Dependency{Source: "../shared"})
		b := packageDir(t, root, "b", artifact.Dependency{Source: "../shared"})
		home := t.TempDir()
		_, pkgs, e := (Resolver{Home: home}).Resolve(context.Background(), []Request{{Source: a, Harness: "claude"}, {Source: b, Harness: "claude"}}, Lock{}, nil)
		if e != nil {
			t.Fatal(e)
		}
		if got := count(t, home); got != 3 {
			t.Fatalf("duplicate dependency extraction leaked: %d trees for 3 packages", got)
		}
		for _, p := range pkgs {
			if e = p.Close(); e != nil {
				t.Fatal(e)
			}
		}
		if count(t, home) != 0 {
			t.Fatal("returned package cleanup failed")
		}
	})
	t.Run("error", func(t *testing.T) {
		root := t.TempDir()
		packageDir(t, root, "shared")
		a := packageDir(t, root, "a", artifact.Dependency{Source: "../shared"}, artifact.Dependency{Source: "../missing"})
		home := t.TempDir()
		if _, _, e := (Resolver{Home: home}).Resolve(context.Background(), []Request{{Source: a, Harness: "claude"}}, Lock{}, nil); e == nil {
			t.Fatal("expected missing dependency")
		}
		if got := count(t, home); got != 0 {
			t.Fatalf("failed resolution leaked %d extraction trees", got)
		}
	})
}
