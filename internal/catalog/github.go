// Package catalog distributes Atlas releases through GitHub's native releases API.
package catalog

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/project"
	"github.com/google/go-github/v82/github"
)

type Config struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
	TokenEnv   string `json:"tokenEnv,omitempty"`
	APIURL     string `json:"apiUrl,omitempty"`
	UploadURL  string `json:"uploadUrl,omitempty"`
}
type Settings struct {
	SchemaVersion int               `json:"schemaVersion"`
	Default       string            `json:"default,omitempty"`
	Catalogs      map[string]Config `json:"catalogs"`
}
type Release struct {
	SchemaVersion int               `json:"schemaVersion"`
	Manifest      artifact.Manifest `json:"manifest"`
	Digest        string            `json:"sha256"`
	SourceCommit  string            `json:"sourceCommit,omitempty"`
}
type Entry struct {
	Release
	Tag     string `json:"tag"`
	URL     string `json:"url"`
	AssetID int64  `json:"-"`
}
type Client struct {
	API         *github.Client
	Config      Config
	Owner, Repo string
}

const bodyPrefix = "Atlas package release v1\n\n```json\n"

func Load(home string) (Settings, error) {
	s := Settings{SchemaVersion: 1, Catalogs: map[string]Config{}}
	b, e := os.ReadFile(filepath.Join(home, "catalogs.json"))
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if s.SchemaVersion != 1 || s.Catalogs == nil {
		return s, errors.New("invalid catalog settings")
	}
	return s, nil
}
func Save(home string, s Settings) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return project.Atomic(filepath.Join(home, "catalogs.json"), append(b, '\n'), 0600)
}
func (s Settings) Resolve(name string) (string, Config, error) {
	if name == "" {
		name = s.Default
	}
	c, ok := s.Catalogs[name]
	if !ok {
		return "", c, errors.New("configure a catalog first: atlas catalog add NAME OWNER/REPO [--host HOST]")
	}
	return name, c, nil
}
func Validate(c Config) error {
	parts := strings.Split(c.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(c.Repository, " ?#\\\t\n") {
		return errors.New("repository must be OWNER/REPO")
	}
	if c.Host == "" || strings.ContainsAny(c.Host, "/?#@\\ \t\n") {
		return errors.New("host must be a hostname, e.g. github.com")
	}
	for _, v := range []string{c.APIURL, c.UploadURL} {
		if v != "" {
			u, e := url.Parse(v)
			if e != nil || u.Host == "" || u.User != nil || u.Scheme != "https" {
				return errors.New("custom GitHub API/upload URLs must be HTTPS")
			}
		}
	}
	return nil
}

// Credentials remain outside package/project files. gh is an optional bridge
// to an existing login; all repository operations use the Go SDK directly.
func token(ctx context.Context, home string, c Config) (string, error) {
	if c.TokenEnv != "" {
		t := os.Getenv(c.TokenEnv)
		if t == "" {
			return "", fmt.Errorf("credential environment variable %s is unset", c.TokenEnv)
		}
		return t, nil
	}
	names := []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	if c.Host == "github.com" {
		names = []string{"GH_TOKEN", "GITHUB_TOKEN"}
	}
	for _, n := range names {
		if t := os.Getenv(n); t != "" {
			return t, nil
		}
	}
	b, e := os.ReadFile(filepath.Join(home, "github-credentials.json"))
	if e == nil {
		var stored map[string]string
		if e = json.Unmarshal(b, &stored); e != nil {
			return "", errors.New("invalid GitHub credential file")
		}
		if t := stored[c.Host]; t != "" {
			return t, nil
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if path, e := exec.LookPath("gh"); e == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		b, e := exec.CommandContext(ctx, path, "auth", "token", "--hostname", c.Host).Output()
		if e == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	return "", nil // public catalogs are readable anonymously
}
func StoreToken(home, host, value string) error {
	if e := Validate(Config{Host: host, Repository: "owner/repo"}); e != nil {
		return e
	}
	if strings.TrimSpace(value) == "" {
		return errors.New("empty token")
	}
	path := filepath.Join(home, "github-credentials.json")
	m := map[string]string{}
	if b, e := os.ReadFile(path); e == nil {
		if e = json.Unmarshal(b, &m); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if m == nil {
		return errors.New("invalid GitHub credential file: expected an object")
	}
	m[host] = strings.TrimSpace(value)
	b, _ := json.Marshal(m)
	return project.Atomic(path, b, 0600)
}
func New(ctx context.Context, home string, c Config) (*Client, error) {
	if c.Host == "" {
		c.Host = "github.com"
	}
	if e := Validate(c); e != nil {
		return nil, e
	}
	t, e := token(ctx, home, c)
	if e != nil {
		return nil, e
	}
	return newClient(c, t)
}

// AuthenticateToken validates the supplied credential before it is persisted.
// Ambient GH_TOKEN, tokenEnv, stored credentials and gh login are deliberately
// not consulted: the account returned belongs to this exact supplied token.
func AuthenticateToken(ctx context.Context, c Config, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty token")
	}
	client, e := newClient(c, value)
	if e != nil {
		return "", e
	}
	user, _, e := client.API.Users.Get(ctx, "")
	if e != nil {
		return "", e
	}
	if user.GetLogin() == "" {
		return "", errors.New("GitHub authentication returned no account")
	}
	return user.GetLogin(), nil
}

func newClient(c Config, t string) (*Client, error) {
	if c.Host == "" {
		c.Host = "github.com"
	}
	if e := Validate(c); e != nil {
		return nil, e
	}
	httpClient := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// The SDK's auth transport adds Authorization on every request, including
		// redirects. Constrain API redirects before that transport can run.
		if len(via) == 0 || len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host || req.URL.User != nil {
			return errors.New("refusing cross-origin GitHub API redirect")
		}
		return nil
	}}
	api := github.NewClient(httpClient)
	if t != "" {
		api = api.WithAuthToken(t)
	}
	if c.Host != "github.com" {
		var e error
		api, e = api.WithEnterpriseURLs("https://"+c.Host, "https://"+c.Host)
		if e != nil {
			return nil, e
		}
	}
	if c.APIURL != "" {
		api.BaseURL, _ = url.Parse(strings.TrimRight(c.APIURL, "/") + "/")
	}
	if c.UploadURL != "" {
		api.UploadURL, _ = url.Parse(strings.TrimRight(c.UploadURL, "/") + "/")
	}
	parts := strings.Split(c.Repository, "/")
	return &Client{api, c, parts[0], parts[1]}, nil
}
func Tag(id, version string) string {
	return "atlas-" + base64.RawURLEncoding.EncodeToString([]byte(id)) + "-v" + version
}
func parseRelease(r *github.RepositoryRelease) (Entry, error) {
	var entry Entry
	if r.GetDraft() {
		return entry, errors.New("draft release")
	}
	body := r.GetBody()
	if !strings.HasPrefix(body, bodyPrefix) {
		return entry, errors.New("not an Atlas package release")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(body, bodyPrefix), "\n```\n")
	if e := json.Unmarshal([]byte(raw), &entry.Release); e != nil {
		return entry, e
	}
	if entry.SchemaVersion != 1 || len(entry.Digest) != 64 {
		return entry, errors.New("invalid Atlas release metadata")
	}
	if _, e := hex.DecodeString(entry.Digest); e != nil {
		return entry, errors.New("invalid Atlas release digest")
	}
	if e := artifact.Validate(entry.Manifest); e != nil {
		return entry, e
	}
	if r.GetTagName() != Tag(entry.Manifest.ID, entry.Manifest.Version) {
		return entry, errors.New("release tag does not match package identity")
	}
	for _, a := range r.Assets {
		if a.GetName() == "package.tar.gz" {
			entry.AssetID = a.GetID()
		}
	}
	if entry.AssetID == 0 {
		return entry, errors.New("release has no package archive")
	}
	entry.Tag = r.GetTagName()
	entry.URL = r.GetHTMLURL()
	return entry, nil
}
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	out := []Entry{}
	opts := &github.ListOptions{PerPage: 100}
	for {
		rs, res, e := c.API.Repositories.ListReleases(ctx, c.Owner, c.Repo, opts)
		if e != nil {
			return nil, e
		}
		for _, r := range rs {
			if strings.HasPrefix(r.GetTagName(), "atlas-") && !r.GetDraft() {
				v, e := parseRelease(r)
				if e != nil {
					return nil, fmt.Errorf("invalid catalog release %s: %w", r.GetTagName(), e)
				}
				out = append(out, v)
			}
		}
		if res.NextPage == 0 {
			break
		}
		opts.Page = res.NextPage
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Manifest, out[j].Manifest
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return CompareVersions(a.Version, b.Version) > 0
	})
	return out, nil
}
func (c *Client) Get(ctx context.Context, id, version string) (Entry, error) {
	r, _, e := c.API.Repositories.GetReleaseByTag(ctx, c.Owner, c.Repo, Tag(id, version))
	if e != nil {
		return Entry{}, e
	}
	entry, e := parseRelease(r)
	if e == nil && (entry.Manifest.ID != id || entry.Manifest.Version != version) {
		return Entry{}, errors.New("GitHub release identity differs from requested package")
	}
	return entry, e
}
func (c *Client) Latest(ctx context.Context, id string) (Entry, error) {
	entries, e := c.List(ctx)
	if e != nil {
		return Entry{}, e
	}
	for _, v := range entries {
		if v.Manifest.ID == id && !strings.Contains(v.Manifest.Version, "-") {
			return v, nil
		}
	}
	return Entry{}, fmt.Errorf("no stable release for %s", id)
}
func (c *Client) Download(ctx context.Context, entry Entry) ([]byte, error) {
	// Authentication goes only to the API endpoint. Signed redirects are fetched
	// with a separate client which has no Authorization transport.
	rc, redirect, e := c.API.Repositories.DownloadReleaseAsset(ctx, c.Owner, c.Repo, entry.AssetID, nil)
	if e != nil {
		return nil, e
	}
	if redirect != "" {
		u, e := url.Parse(redirect)
		if e != nil || u.Scheme != "https" || u.User != nil {
			return nil, errors.New("unsafe GitHub asset redirect")
		}
		req, e := http.NewRequestWithContext(ctx, "GET", redirect, nil)
		if e != nil {
			return nil, e
		}
		res, e := (&http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 5 {
				return errors.New("unsafe asset redirect")
			}
			return nil
		}}).Do(req)
		if e != nil {
			return nil, e
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			return nil, fmt.Errorf("GitHub asset download: HTTP %d", res.StatusCode)
		}
		rc = res.Body
	}
	if rc == nil {
		return nil, errors.New("missing GitHub asset response")
	}
	defer rc.Close()
	b, e := io.ReadAll(io.LimitReader(rc, artifact.MaxSize+1))
	if e != nil {
		return nil, e
	}
	if len(b) > artifact.MaxSize || artifact.Hash(b) != entry.Digest {
		return nil, errors.New("GitHub package integrity check failed")
	}
	return b, nil
}
func (c *Client) Publish(ctx context.Context, m artifact.Manifest, data []byte, commit string) (Entry, error) {
	if e := artifact.Validate(m); e != nil {
		return Entry{}, e
	}
	tag := Tag(m.ID, m.Version)
	if _, _, e := c.API.Repositories.GetReleaseByTag(ctx, c.Owner, c.Repo, tag); e == nil {
		return Entry{}, fmt.Errorf("release %s already exists; publish a new version", tag)
	} else {
		var ge *github.ErrorResponse
		if !errors.As(e, &ge) || ge.Response.StatusCode != 404 {
			return Entry{}, e
		}
	}
	rel := Release{SchemaVersion: 1, Manifest: m, Digest: artifact.Hash(data), SourceCommit: commit}
	b, _ := json.MarshalIndent(rel, "", "  ")
	body := bodyPrefix + string(b) + "\n```\n"
	r, _, e := c.API.Repositories.CreateRelease(ctx, c.Owner, c.Repo, &github.RepositoryRelease{TagName: github.Ptr(tag), Name: github.Ptr(m.ID + "@" + m.Version), Body: github.Ptr(body), Draft: github.Ptr(true), Prerelease: github.Ptr(strings.Contains(m.Version, "-"))})
	if e != nil {
		return Entry{}, e
	}
	// A failed upload leaves an identifiable draft for recovery; published releases
	// are never overwritten or deleted as an automatic error-handling step.
	f, e := os.CreateTemp("", "atlas-publish-*.tar.gz")
	if e != nil {
		return Entry{}, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(data); e != nil {
		return Entry{}, e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return Entry{}, e
	}
	_, _, e = c.API.Repositories.UploadReleaseAsset(ctx, c.Owner, c.Repo, r.GetID(), &github.UploadOptions{Name: "package.tar.gz", MediaType: "application/gzip"}, f)
	if e != nil {
		return Entry{}, fmt.Errorf("upload failed; draft release %d retained: %w", r.GetID(), e)
	}
	r, _, e = c.API.Repositories.EditRelease(ctx, c.Owner, c.Repo, r.GetID(), &github.RepositoryRelease{Draft: github.Ptr(false)})
	if e != nil {
		return Entry{}, e
	}
	return parseRelease(r)
}
func (c *Client) Create(ctx context.Context, private bool) error {
	u, _, e := c.API.Users.Get(ctx, "")
	if e != nil {
		return e
	}
	org := c.Owner
	if strings.EqualFold(u.GetLogin(), c.Owner) {
		org = ""
	}
	_, _, e = c.API.Repositories.Create(ctx, org, &github.Repository{Name: github.Ptr(c.Repo), Private: github.Ptr(private), AutoInit: github.Ptr(true), Description: github.Ptr("Versioned native harness packages distributed by Atlas")})
	return e
}

func CompareVersions(a, b string) int {
	split := func(s string) ([]string, string) {
		parts := strings.SplitN(s, "-", 2)
		pre := ""
		if len(parts) > 1 {
			pre = parts[1]
		}
		return strings.Split(parts[0], "."), pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	for i := range an {
		if i >= len(bn) {
			return 1
		}
		if c := compareNumeric(an[i], bn[i]); c != 0 {
			return c
		}
	}
	if len(an) < len(bn) {
		return -1
	}
	if ap == bp {
		return 0
	}
	if ap == "" {
		return 1
	}
	if bp == "" {
		return -1
	}
	aps, bps := strings.Split(ap, "."), strings.Split(bp, ".")
	for i, av := range aps {
		if i >= len(bps) {
			return 1
		}
		bv := bps[i]
		an, bn := numeric(av), numeric(bv)
		var c int
		switch {
		case an && bn:
			c = compareNumeric(av, bv)
		case an:
			c = -1
		case bn:
			c = 1
		default:
			c = strings.Compare(av, bv)
		}
		if c != 0 {
			return c
		}
	}
	if len(aps) < len(bps) {
		return -1
	}
	return 0
}
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func compareNumeric(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
