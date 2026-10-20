package catalog

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/google/go-github/v82/github"
)

func release(id, version string, data []byte) *github.RepositoryRelease {
	m := artifact.Manifest{SchemaVersion: 1, ID: id, Version: version, Kind: "bundle"}
	b, _ := json.Marshal(Release{SchemaVersion: 1, Manifest: m, Digest: artifact.Hash(data)})
	return &github.RepositoryRelease{ID: github.Ptr(int64(10)), TagName: github.Ptr(Tag(id, version)), Body: github.Ptr(bodyPrefix + string(b) + "\n```\n"), HTMLURL: github.Ptr("https://github.example/team/catalog/releases/tag/" + Tag(id, version)), Assets: []*github.ReleaseAsset{{ID: github.Ptr(int64(20)), Name: github.Ptr("package.tar.gz")}}}
}
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("SDK authentication missing for %s", r.URL.Path)
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	api := github.NewClient(server.Client()).WithAuthToken("test-token")
	api.BaseURL, _ = url.Parse(server.URL + "/")
	api.UploadURL, _ = url.Parse(server.URL + "/")
	return &Client{API: api, Owner: "team", Repo: "catalog", Config: Config{Host: "github.example", Repository: "team/catalog"}}
}
func respond(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if e := json.NewEncoder(w).Encode(value); e != nil {
		t.Error(e)
	}
}

func TestSDKCatalogPaginationAndLatest(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/team/catalog/releases" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		if r.URL.Query().Get("page") == "2" {
			respond(t, w, []*github.RepositoryRelease{release("@team/research", "2.10.0", nil), release("@team/research", "3.0.0-beta.1", nil)})
			return
		}
		w.Header().Set("Link", fmt.Sprintf("<http://%s/repos/team/catalog/releases?page=2>; rel=\"next\"", r.Host))
		draft := release("@team/research", "9.0.0", nil)
		draft.Draft = github.Ptr(true)
		respond(t, w, []*github.RepositoryRelease{release("@team/research", "2.9.0", nil), draft, {TagName: github.Ptr("ordinary-release")}})
	})
	entries, e := c.List(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 3 || entries[0].Manifest.Version != "3.0.0-beta.1" {
		t.Fatalf("bad list: %#v", entries)
	}
	latest, e := c.Latest(context.Background(), "@team/research")
	if e != nil || latest.Manifest.Version != "2.10.0" {
		t.Fatalf("latest: %#v %v", latest, e)
	}
	if calls != 4 {
		t.Fatalf("pagination calls=%d", calls)
	}
}

func TestGetChecksRequestedIdentity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		respond(t, w, release("@other/unexpected", "1.0.0", nil))
	})
	if _, e := c.Get(context.Background(), "@team/expected", "1.0.0"); e == nil {
		t.Fatal("accepted a different valid package than requested")
	}
}

func TestSDKDownloadIntegrityAndUnsafeRedirect(t *testing.T) {
	for _, mode := range []string{"valid", "bad-digest", "http-redirect"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("immutable package bytes")
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/team/catalog/releases/assets/20" {
					t.Errorf("wrong asset endpoint: %s", r.URL.Path)
				}
				if mode == "http-redirect" {
					w.Header().Set("Location", "http://untrusted.example/package")
					w.WriteHeader(http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(data)
			})
			digest := artifact.Hash(data)
			if mode == "bad-digest" {
				digest = strings.Repeat("0", 64)
			}
			got, e := c.Download(context.Background(), Entry{Release: Release{Digest: digest}, AssetID: 20})
			if mode == "valid" {
				if e != nil || string(got) != string(data) {
					t.Fatalf("download %q %v", got, e)
				}
			} else if e == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestSDKPublishDraftUploadThenActivate(t *testing.T) {
	data := []byte("archive data")
	var stored *github.RepositoryRelease
	var calls []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET":
			http.Error(w, `{"message":"Not Found"}`, 404)
		case r.Method == "POST" && r.URL.Path == "/repos/team/catalog/releases":
			if e := json.NewDecoder(r.Body).Decode(&stored); e != nil {
				t.Error(e)
			}
			if !stored.GetDraft() {
				t.Error("release made public before upload")
			}
			stored.ID = github.Ptr(int64(10))
			respond(t, w, stored)
		case r.Method == "POST" && r.URL.Path == "/repos/team/catalog/releases/10/assets":
			b, e := io.ReadAll(r.Body)
			if e != nil || string(b) != string(data) {
				t.Errorf("upload data mismatch %q %v", b, e)
			}
			if r.URL.Query().Get("name") != "package.tar.gz" {
				t.Error("wrong asset name")
			}
			stored.Assets = []*github.ReleaseAsset{{ID: github.Ptr(int64(20)), Name: github.Ptr("package.tar.gz")}}
			respond(t, w, stored.Assets[0])
		case r.Method == "PATCH":
			var input github.RepositoryRelease
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				t.Error(e)
			}
			if input.GetDraft() {
				t.Error("activation still draft")
			}
			stored.Draft = github.Ptr(false)
			respond(t, w, stored)
		default:
			t.Errorf("unexpected API operation %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	m := artifact.Manifest{SchemaVersion: 1, ID: "@team/research", Version: "1.0.0", Kind: "bundle"}
	entry, e := c.Publish(context.Background(), m, data, strings.Repeat("a", 40))
	if e != nil {
		t.Fatal(e)
	}
	if entry.Digest != artifact.Hash(data) || entry.SourceCommit != strings.Repeat("a", 40) || len(calls) != 4 {
		t.Fatalf("published unexpected metadata/calls %#v %#v", entry, calls)
	}
}

func TestPublishDoesNotOverwriteExistingReleaseOrActivateFailedUpload(t *testing.T) {
	for _, mode := range []string{"exists", "upload-fails"} {
		t.Run(mode, func(t *testing.T) {
			mutations := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if mode == "exists" {
						respond(t, w, release("x", "1.0.0", nil))
					} else {
						http.Error(w, `{"message":"Not Found"}`, 404)
					}
					return
				}
				mutations++
				if r.Method == "POST" && r.URL.Path == "/repos/team/catalog/releases" {
					respond(t, w, &github.RepositoryRelease{ID: github.Ptr(int64(10)), Draft: github.Ptr(true)})
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/assets") {
					http.Error(w, `{"message":"storage unavailable"}`, 500)
					return
				}
				t.Errorf("unexpected cleanup or activation %s %s", r.Method, r.URL.Path)
				http.Error(w, "unexpected", 500)
			})
			_, e := c.Publish(context.Background(), artifact.Manifest{SchemaVersion: 1, ID: "x", Version: "1.0.0", Kind: "bundle"}, []byte("archive"), "")
			if e == nil {
				t.Fatal("publish should fail")
			}
			if mode == "exists" && mutations != 0 {
				t.Fatal("mutated existing release")
			}
			if mode == "upload-fails" && !strings.Contains(e.Error(), "draft release") {
				t.Fatalf("failed draft not identified: %v", e)
			}
		})
	}
}

func TestCredentialStorageAndEnvironmentSelection(t *testing.T) {
	home := t.TempDir()
	if e := StoreToken(home, "github.company.example", "a-test-token"); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(filepath.Join(home, "github-credentials.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v %v", st, e)
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	got, e := token(context.Background(), home, Config{Host: "github.company.example"})
	if e != nil || got != "a-test-token" {
		t.Fatalf("stored token: %q %v", got, e)
	}
	t.Setenv("ATLAS_TEST_TOKEN", "from-env")
	got, e = token(context.Background(), home, Config{Host: "github.company.example", TokenEnv: "ATLAS_TEST_TOKEN"})
	if e != nil || got != "from-env" {
		t.Fatal("explicit credential environment not used")
	}
	t.Setenv("ATLAS_TEST_TOKEN", "")
	if _, e = token(context.Background(), home, Config{Host: "github.company.example", TokenEnv: "ATLAS_TEST_TOKEN"}); e == nil {
		t.Fatal("missing explicitly selected credentials silently accepted")
	}
}

func TestSemanticVersionOrdering(t *testing.T) {
	for _, pair := range [][2]string{{"1.0.0-beta.2", "1.0.0-beta.10"}, {"1.0.0-1", "1.0.0-alpha"}, {"1.0.0-alpha", "1.0.0-alpha.1"}, {"1.0.0-alpha", "1.0.0"}, {"1.9.0", "1.10.0"}} {
		if CompareVersions(pair[0], pair[1]) >= 0 {
			t.Errorf("expected %s before %s", pair[0], pair[1])
		}
	}
}

func TestAuthenticatedSDKRefusesCrossOriginRedirect(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls++
		t.Error("authenticated SDK followed cross-origin redirect")
		respond(t, w, []any{})
	}))
	defer foreign.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("initial SDK request missing auth")
		}
		w.Header().Set("Location", foreign.URL+"/stolen")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	previous := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	t.Setenv("ATLAS_TEST_TOKEN", "test-token")
	c, e := New(context.Background(), t.TempDir(), Config{Host: "github.company.example", Repository: "team/catalog", TokenEnv: "ATLAS_TEST_TOKEN", APIURL: server.URL})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.List(context.Background()); e == nil || !strings.Contains(e.Error(), "cross-origin") {
		t.Fatalf("redirect should fail: %v", e)
	}
	if foreignCalls != 0 {
		t.Fatal("credential origin boundary crossed")
	}
}

func TestCredentialStorageRejectsNullMap(t *testing.T) {
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "github-credentials.json"), []byte("null"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := StoreToken(home, "github.company.example", "token"); e == nil {
		t.Fatal("null credential map should fail")
	}
}

func TestAuthenticateTokenUsesSuppliedCredential(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected auth path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer explicit-token" {
			http.Error(w, `{"message":"Bad credentials"}`, 401)
			return
		}
		respond(t, w, map[string]string{"login": "intended-user"})
	}))
	defer server.Close()
	previous := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	t.Setenv("GH_TOKEN", "different-ambient-token")
	t.Setenv("ATLAS_TEST_TOKEN", "different-configured-token")
	cfg := Config{Host: "github.com", Repository: "owner/catalog", TokenEnv: "ATLAS_TEST_TOKEN", APIURL: server.URL}
	login, e := AuthenticateToken(context.Background(), cfg, " explicit-token\n")
	if e != nil || login != "intended-user" {
		t.Fatalf("wrong authentication identity: %q %v", login, e)
	}
	if _, e = AuthenticateToken(context.Background(), cfg, "invalid-token"); e == nil {
		t.Fatal("invalid explicit token accepted")
	}
	if _, e = AuthenticateToken(context.Background(), cfg, ""); e == nil {
		t.Fatal("empty token accepted")
	}
}
