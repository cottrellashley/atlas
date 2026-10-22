package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/assets"
	"github.com/cottrellashley/atlas/internal/catalog"
	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/install"
	"github.com/cottrellashley/atlas/internal/project"
	"github.com/cottrellashley/atlas/internal/resolve"
)

func (a *App) resolver(name string) (resolve.Resolver, error) {
	s, e := catalog.Load(a.Home)
	return resolve.Resolver{Home: a.Home, Catalogs: s, Catalog: name}, e
}
func (a *App) catalog(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas catalog add|list|remove|create")
	}
	f := a.flags("catalog " + args[0])
	host := f.String("host", "github.com", "GitHub hostname")
	tok := f.String("token-env", "", "credential environment variable")
	api := f.String("api-url", "", "custom GitHub REST base URL")
	upload := f.String("upload-url", "", "custom GitHub upload base URL")
	def := f.Bool("default", false, "make default catalog")
	public := f.Bool("public", false, "create a public repository (default private)")
	j := f.Bool("json", false, "JSON output")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	s, e := catalog.Load(a.Home)
	if e != nil {
		return e
	}
	switch args[0] {
	case "list":
		if f.NArg() != 0 {
			return errors.New("catalog list takes no arguments")
		}
		if *j {
			return a.json(s)
		}
		names := []string{}
		for k := range s.Catalogs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			c := s.Catalogs[name]
			mark := ""
			if name == s.Default {
				mark = " (default)"
			}
			fmt.Fprintf(a.Out, "%s\t%s/%s%s\n", name, c.Host, c.Repository, mark)
		}
		return nil
	case "add", "create":
		if f.NArg() != 2 {
			return errors.New("usage: atlas catalog add|create NAME OWNER/REPO [--host HOST]")
		}
		name := f.Arg(0)
		if strings.ContainsAny(name, ":/#@ \t\n") || name == "" {
			return errors.New("invalid catalog name")
		}
		c := catalog.Config{Host: *host, Repository: f.Arg(1), TokenEnv: *tok, APIURL: *api, UploadURL: *upload}
		if e = catalog.Validate(c); e != nil {
			return e
		}
		client, e := catalog.New(ctx, a.Home, c)
		if e != nil {
			return e
		}
		if args[0] == "create" {
			if e = client.Create(ctx, !*public); e != nil {
				return e
			}
		} else {
			if _, _, e = client.API.Repositories.Get(ctx, client.Owner, client.Repo); e != nil {
				return e
			}
		}
		s.Catalogs[name] = c
		if *def || s.Default == "" {
			s.Default = name
		}
		if e = catalog.Save(a.Home, s); e != nil {
			return e
		}
		if *j {
			return a.json(map[string]any{"name": name, "catalog": c, "default": s.Default == name})
		}
		fmt.Fprintf(a.Out, "Catalog %s: https://%s/%s\n", name, c.Host, c.Repository)
		return nil
	case "remove":
		if f.NArg() != 1 {
			return errors.New("catalog remove requires a name")
		}
		delete(s.Catalogs, f.Arg(0))
		if s.Default == f.Arg(0) {
			s.Default = ""
		}
		return catalog.Save(a.Home, s)
	default:
		return fmt.Errorf("unknown catalog command %q", args[0])
	}
}
func (a *App) github(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas github login --token-stdin | status [--host HOST]")
	}
	f := a.flags("github " + args[0])
	host := f.String("host", "github.com", "GitHub hostname")
	stdin := f.Bool("token-stdin", false, "read token from standard input")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected GitHub arguments")
	}
	switch args[0] {
	case "login":
		if !*stdin {
			return errors.New("use --token-stdin, or set GH_TOKEN/GH_ENTERPRISE_TOKEN")
		}
		b, e := io.ReadAll(io.LimitReader(a.In, 16385))
		if e != nil {
			return e
		}
		if len(b) > 16384 {
			return errors.New("token is too large")
		}
		username, err := catalog.AuthenticateToken(ctx, catalog.Config{Host: *host, Repository: "owner/repo"}, string(b))
		if err != nil {
			return err
		}
		if e = catalog.StoreToken(a.Home, *host, string(b)); e != nil {
			return e
		}
		fmt.Fprintf(a.Out, "Authenticated to %s as %s.\n", *host, username)
		return nil
	case "status":
	default:
		return errors.New("unknown GitHub command")
	}
	c, e := catalog.New(ctx, a.Home, catalog.Config{Host: *host, Repository: "owner/repo"})
	if e != nil {
		return e
	}
	u, _, e := c.API.Users.Get(ctx, "")
	if e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "Authenticated to %s as %s.\n", *host, u.GetLogin())
	return nil
}

func packageRoot(cwd, scope string) (string, error) {
	if scope == "user" {
		for _, key := range []string{"CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE", "XDG_CONFIG_HOME", "OPENCODE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				return "", fmt.Errorf("user scope with %s override is not supported; use project scope or the native installer", key)
			}
		}
		return os.UserHomeDir()
	}
	if scope != "project" {
		return "", errors.New("scope must be project or user")
	}
	p, root, e := project.Load(cwd)
	if e != nil {
		return "", e
	}
	if p != nil {
		return root, nil
	}
	return filepath.Abs(cwd)
}
func (a *App) packageCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas package search|add|sync|status|update|remove|recover|validate|publish")
	}
	op := args[0]
	f := a.flags("package " + op)
	id := f.String("harness", "", "configured harness")
	cwd := f.String("cwd", ".", "project directory")
	scope := f.String("scope", "project", "project or user")
	cat := f.String("catalog", "", "catalog name")
	dry := f.Bool("dry-run", false, "preview changes")
	j := f.Bool("json", false, "JSON output")
	locked := f.Bool("locked", true, "require matching lock on sync")
	version := f.String("version", "", "new exact version for update")
	commit := f.String("source-commit", "", "reviewed source commit for publication")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	resolver, e := a.resolver(*cat)
	if e != nil {
		return e
	}
	if op == "search" {
		if f.NArg() > 1 {
			return errors.New("search accepts one query")
		}
		_, cfg, e := resolver.Catalogs.Resolve(*cat)
		if e != nil {
			return e
		}
		client, e := catalog.New(ctx, a.Home, cfg)
		if e != nil {
			return e
		}
		entries, e := client.List(ctx)
		if e != nil {
			return e
		}
		filtered := []catalog.Entry{}
		for _, entry := range entries {
			if strings.Contains(strings.ToLower(entry.Manifest.ID+" "+entry.Manifest.Description), strings.ToLower(f.Arg(0))) {
				filtered = append(filtered, entry)
			}
		}
		if *j {
			return a.json(filtered)
		}
		for _, v := range filtered {
			fmt.Fprintf(a.Out, "%s@%s\t%s\t%s\n", v.Manifest.ID, v.Manifest.Version, v.Manifest.Kind, v.Manifest.Description)
		}
		return nil
	}
	if op == "validate" || op == "publish" {
		if f.NArg() != 1 {
			return fmt.Errorf("package %s requires a package directory", op)
		}
		m, data, e := artifact.Snapshot(f.Arg(0))
		if e != nil {
			return e
		}
		p, e := artifact.Extract(a.Home, "file:"+f.Arg(0), artifact.Hash(data), data)
		if e != nil {
			return e
		}
		defer p.Close()
		hs := m.Harnesses
		if len(hs) == 0 {
			switch m.Kind {
			case "claude-plugin":
				hs = []string{"claude"}
			case "pi-package":
				hs = []string{"pi"}
			case "opencode-plugin":
				hs = []string{"opencode"}
			default:
				hs = []string{"claude", "pi", "omp", "opencode"}
			}
		}
		for _, h := range hs {
			if _, e = assets.Render(p, h, "project"); e != nil {
				return fmt.Errorf("%s validation: %w", h, e)
			}
		}
		if op == "validate" {
			if *j {
				return a.json(map[string]any{"manifest": m, "sha256": p.Digest, "valid": true})
			}
			fmt.Fprintf(a.Out, "Valid %s@%s (%s), SHA-256 %s\n", m.ID, m.Version, m.Kind, p.Digest)
			return nil
		}
		for _, dep := range m.Dependencies {
			if strings.HasPrefix(dep.Source, "file:") || strings.HasPrefix(dep.Source, ".") || filepath.IsAbs(dep.Source) {
				return errors.New("published dependencies must be catalog packages with exact versions")
			}
			_, v := resolve.SplitID(dep.Source)
			if err := artifact.Validate(artifact.Manifest{SchemaVersion: 1, ID: "dependency", Version: v, Kind: "bundle"}); err != nil {
				return errors.New("published dependencies require exact versions")
			}
		}
		if *dry {
			return a.json(map[string]any{"manifest": m, "sha256": p.Digest, "bytes": len(data), "dryRun": true})
		}
		_, cfg, e := resolver.Catalogs.Resolve(*cat)
		if e != nil {
			return e
		}
		client, e := catalog.New(ctx, a.Home, cfg)
		if e != nil {
			return e
		}
		entry, e := client.Publish(ctx, m, data, *commit)
		if e != nil {
			return e
		}
		if *j {
			return a.json(entry)
		}
		fmt.Fprintf(a.Out, "Published %s@%s\n%s\n", m.ID, m.Version, entry.URL)
		return nil
	}
	root, e := packageRoot(*cwd, *scope)
	if e != nil {
		return e
	}
	if op == "recover" {
		if f.NArg() != 0 {
			return errors.New("recover takes no arguments")
		}
		if *dry {
			return errors.New("recover does not support --dry-run")
		}
		if e = install.Recover(root); e != nil {
			return e
		}
		fmt.Fprintln(a.Out, "Package installation recovery complete.")
		return nil
	}
	declaration, old, e := resolve.Read(root)
	if e != nil {
		return e
	}
	if e = checkPackageScope(root, *scope, declaration, old); e != nil {
		return e
	}
	if op == "status" || op == "list" {
		if f.NArg() != 0 {
			return errors.New("status takes no arguments")
		}
		drift, e := install.Status(root)
		if e != nil {
			return e
		}
		installed, err := install.Contributions(root)
		if err != nil {
			return err
		}
		owned := map[string]install.Contribution{}
		for _, contribution := range installed {
			owned[contribution.PackageID] = contribution
		}
		for _, p := range old.Packages {
			if _, ok := owned[resolve.Key(p.Harness, p.ID)]; !ok {
				drift = append(drift, install.Drift{Path: ".atlas/install-receipt.json", Packages: []string{resolve.Key(p.Harness, p.ID)}, Reason: "package has not been installed here; run atlas package sync --locked"})
			} else {
				m := owned[resolve.Key(p.Harness, p.ID)].Metadata
				if m["version"] != p.Version || m["kind"] != p.Kind || m["source"] != p.Source || m["sha256"] != p.Digest || m["harness"] != p.Harness {
					drift = append(drift, install.Drift{Path: ".atlas/install-receipt.json", Packages: []string{resolve.Key(p.Harness, p.ID)}, Reason: "installed release differs from lock; run atlas package sync --locked"})
				}
			}
		}
		expected := map[string]bool{}
		for _, p := range old.Packages {
			expected[resolve.Key(p.Harness, p.ID)] = true
		}
		for key := range owned {
			if !expected[key] {
				drift = append(drift, install.Drift{Path: ".atlas/install-receipt.json", Packages: []string{key}, Reason: "installed package is absent from lock; run atlas package sync --locked"})
			}
		}
		if *j {
			e = a.json(map[string]any{"packages": old.Packages, "drift": drift, "locked": resolve.Matches(declaration, old)})
		} else {
			for _, v := range old.Packages {
				fmt.Fprintf(a.Out, "%s@%s\t%s\t%s\n", v.ID, v.Version, v.Harness, v.Kind)
			}
			for _, v := range drift {
				fmt.Fprintf(a.Out, "DRIFT %s%s: %s\n", v.Path, v.Pointer, v.Reason)
			}
		}
		if e != nil {
			return e
		}
		if len(drift) > 0 {
			return errors.New("managed package resources have changed; preserve or restore those edits before updating")
		}
		if !resolve.Matches(declaration, old) {
			return errors.New("declaration differs from lock; run package sync --locked=false")
		}
		return nil
	}
	requests := append([]resolve.Request{}, declaration.Packages...)
	updates := map[string]bool{}
	switch op {
	case "add", "install":
		if f.NArg() != 1 {
			return errors.New("package add requires a source directory or package ID")
		}
		cfg, _, _, e := a.selection(*id, *cwd)
		if e != nil {
			return e
		}
		if *scope == "user" {
			if e = checkUserEnvironment(cfg); e != nil {
				return e
			}
		}
		p, e := resolver.Fetch(ctx, f.Arg(0))
		if e != nil {
			return e
		}
		defer p.Close()
		source := f.Arg(0)
		if strings.HasPrefix(p.Source, "file:") {
			source = p.Source
		}
		found := false
		for i, v := range requests {
			if v.Harness == cfg.Adapter && (v.ID == p.Manifest.ID || v.Source == source) {
				requests[i] = resolve.Request{Source: source, Harness: cfg.Adapter, HarnessID: cfg.ID, ID: p.Manifest.ID}
				found = true
			}
		}
		if !found {
			requests = append(requests, resolve.Request{Source: source, Harness: cfg.Adapter, HarnessID: cfg.ID, ID: p.Manifest.ID})
		}
		updates[source] = true
	case "remove", "uninstall":
		if f.NArg() != 1 {
			return errors.New("package remove requires a package ID")
		}
		keep := []resolve.Request{}
		found := false
		adapter := ""
		if *id != "" {
			cfg, _, _, e := a.selection(*id, *cwd)
			if e != nil {
				return e
			}
			adapter = cfg.Adapter
		}
		for _, v := range requests {
			if (v.ID == f.Arg(0) || v.Source == f.Arg(0)) && (adapter == "" || adapter == v.Harness) {
				found = true
				continue
			}
			keep = append(keep, v)
		}
		if !found {
			return errors.New("package is not a direct dependency; remove the bundle that owns it")
		}
		requests = keep
	case "update":
		if *version != "" && f.NArg() != 1 {
			return errors.New("--version requires one package ID")
		}
		if f.NArg() > 1 {
			return errors.New("update accepts at most one package ID")
		}
		found := false
		for i, v := range requests {
			if f.NArg() == 0 || v.ID == f.Arg(0) || v.Source == f.Arg(0) {
				found = true
				if *version != "" {
					if strings.HasPrefix(v.Source, "file:") {
						return errors.New("local packages get their version from atlas-package.json")
					}
					base, _ := resolve.SplitID(v.Source)
					requests[i].Source = base + "@" + *version
				}
				updates[requests[i].Source] = true
			}
		}
		if !found {
			return errors.New("package is not installed as a direct dependency")
		}
	case "sync":
		if f.NArg() != 0 {
			return errors.New("sync takes no arguments")
		}
		if *locked && !resolve.Matches(declaration, old) {
			return errors.New("declaration differs from lock; use --locked=false to resolve changes")
		}
	default:
		return fmt.Errorf("unknown package command %q", op)
	}
	plan, next, e := a.packagePlan(ctx, root, *scope, *id, resolver, requests, old, updates, nil, nil)
	if e != nil {
		return e
	}
	if !*dry {
		if e = install.Apply(root, plan); e != nil {
			return e
		}
	}
	if *j {
		return a.json(map[string]any{"dryRun": *dry, "plan": plan, "lock": next})
	}
	for _, v := range plan.Files {
		fmt.Fprintf(a.Out, "%-10s %s\n", v.Action, v.Path)
	}
	if *dry {
		fmt.Fprintln(a.Out, "Dry run; native project files unchanged.")
	} else {
		fmt.Fprintln(a.Out, "Native resources installed. Launch with atlas tui or the provider's own command; native project trust and permissions still apply.")
	}
	return nil
}
func checkUserEnvironment(cfg harness.Config) error {
	if cfg.Env["HOME"] != "" && cfg.Env["HOME"] != os.Getenv("HOME") {
		return errors.New("user scope cannot target a configured HOME override")
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE", "XDG_CONFIG_HOME", "OPENCODE_CONFIG_DIR"} {
		if cfg.Env[key] != "" {
			return fmt.Errorf("user scope cannot target configured %s override", key)
		}
	}
	if cfg.Adapter == "omp" {
		if _, configured := cfg.Options["profile"]; configured {
			return errors.New("user scope cannot target an Oh-my-pi profile override; use project scope or the native installer")
		}
		for i, arg := range cfg.Command {
			if i == 0 {
				continue
			}
			if arg == "--profile" || strings.HasPrefix(arg, "--profile=") {
				return errors.New("user scope cannot target an Oh-my-pi profile override; use project scope or the native installer")
			}
		}
	}
	return nil
}

func (a *App) skill(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas skill add DIRECTORY|PACKAGE [package flags]")
	}
	if args[0] != "add" && args[0] != "install" {
		return a.packageCommand(ctx, args)
	}
	// Parse only to locate the source while preserving the package flags.
	f := a.flags("skill add")
	h := f.String("harness", "", "harness")
	cwd := f.String("cwd", ".", "directory")
	scope := f.String("scope", "project", "scope")
	cat := f.String("catalog", "", "catalog")
	dry := f.Bool("dry-run", false, "preview")
	j := f.Bool("json", false, "JSON")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("skill add requires a directory or package")
	}
	source := f.Arg(0)
	if _, e := os.Stat(filepath.Join(source, "SKILL.md")); e == nil {
		abs, e := filepath.Abs(source)
		if e != nil {
			return e
		}
		skill, e := assets.ReadSkill(abs, ".")
		if e != nil {
			return e
		}
		dir, e := os.MkdirTemp(filepath.Join(a.Home), "skill-import-")
		if e != nil {
			if e = os.MkdirAll(a.Home, 0700); e != nil {
				return e
			}
			dir, e = os.MkdirTemp(a.Home, "skill-import-")
			if e != nil {
				return e
			}
		}
		if e = copySkill(abs, filepath.Join(dir, skill.Name)); e != nil {
			return e
		}
		m := artifact.Manifest{SchemaVersion: 1, ID: skill.Name, Version: "0.1.0", Kind: "skill", Skills: []string{skill.Name}}
		if e = project.Atomic(filepath.Join(dir, artifact.ManifestName), resolve.Marshal(m), 0600); e != nil {
			return e
		}
		source = dir
	}
	forwarded := []string{"add", source, "--harness", *h, "--cwd", *cwd, "--scope", *scope, "--catalog", *cat}
	if *dry {
		forwarded = append(forwarded, "--dry-run")
	}
	if *j {
		forwarded = append(forwarded, "--json")
	}
	return a.packageCommand(ctx, forwarded)
}
func copySkill(source, dest string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !d.Type().IsRegular() {
			return errors.New("skill resources must be regular files")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

type repeatStrings []string

func (r *repeatStrings) String() string     { return strings.Join(*r, ",") }
func (r *repeatStrings) Set(v string) error { *r = append(*r, v); return nil }
func (a *App) mcp(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas mcp add NAME --command EXECUTABLE [--arg ARG] | --url URL")
	}
	if args[0] != "add" {
		if (args[0] == "remove" || args[0] == "uninstall") && len(args) > 1 && !strings.HasPrefix(args[1], "mcp-") && !strings.HasPrefix(args[1], "@") {
			args = append([]string{}, args...)
			args[1] = "mcp-" + args[1]
		}
		return a.packageCommand(ctx, args)
	}
	f := a.flags("mcp add")
	h := f.String("harness", "", "harness")
	cwd := f.String("cwd", ".", "directory")
	scope := f.String("scope", "project", "scope")
	command := f.String("command", "", "stdio executable")
	url := f.String("url", "", "HTTP MCP URL")
	dry := f.Bool("dry-run", false, "preview")
	j := f.Bool("json", false, "JSON")
	var argv, env, headers repeatStrings
	f.Var(&argv, "arg", "stdio argument (repeatable)")
	f.Var(&env, "env", "NAME=VALUE (use environment references for secrets)")
	f.Var(&headers, "header", "NAME=VALUE (repeatable)")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	if f.NArg() != 1 || (*command == "") == (*url == "") {
		return errors.New("mcp add needs NAME and exactly one of --command or --url")
	}
	if *url != "" && (len(argv) > 0 || len(env) > 0) {
		return errors.New("HTTP MCP does not accept --arg or --env; use --header")
	}
	if *command != "" && len(headers) > 0 {
		return errors.New("stdio MCP does not accept --header; use --env")
	}
	name := f.Arg(0)
	m := artifact.Manifest{SchemaVersion: 1, ID: "mcp-" + name, Version: "0.1.0", Kind: "mcp", MCP: "servers.json"}
	if e := artifact.Validate(m); e != nil {
		return e
	}
	server := map[string]any{}
	if *command != "" {
		server["transport"] = "stdio"
		server["command"] = *command
		if len(argv) > 0 {
			server["args"] = []string(argv)
		}
	} else {
		server["transport"] = "http"
		server["url"] = *url
	}
	for key, values := range map[string]repeatStrings{"env": env, "headers": headers} {
		if len(values) == 0 {
			continue
		}
		pairs := map[string]string{}
		for _, v := range values {
			p := strings.SplitN(v, "=", 2)
			if len(p) != 2 || p[0] == "" {
				return fmt.Errorf("invalid %s entry", key)
			}
			pairs[p[0]] = p[1]
		}
		server[key] = pairs
	}
	if e := os.MkdirAll(a.Home, 0700); e != nil {
		return e
	}
	dir, e := os.MkdirTemp(a.Home, "mcp-import-")
	if e != nil {
		return e
	}
	if e = project.Atomic(filepath.Join(dir, artifact.ManifestName), resolve.Marshal(m), 0600); e != nil {
		return e
	}
	if e = project.Atomic(filepath.Join(dir, "servers.json"), resolve.Marshal(map[string]any{name: server}), 0600); e != nil {
		return e
	}
	forwarded := []string{"add", dir, "--harness", *h, "--cwd", *cwd, "--scope", *scope}
	if *dry {
		forwarded = append(forwarded, "--dry-run")
	}
	if *j {
		forwarded = append(forwarded, "--json")
	}
	return a.packageCommand(ctx, forwarded)
}

func (a *App) packagePlan(ctx context.Context, root, scope, harnessID string, resolver resolve.Resolver, requests []resolve.Request, old resolve.Lock, updates map[string]bool, base, extraMetadata []install.File) (*install.Transaction, resolve.Lock, error) {
	aliases := map[string]string{}
	for _, request := range requests {
		name := request.HarnessID
		if name == "" {
			name = request.Harness
		}
		if previous := aliases[request.Harness]; previous != "" && previous != name {
			return nil, resolve.Lock{}, fmt.Errorf("packages for %s use conflicting configured harnesses %s and %s", request.Harness, previous, name)
		}
		aliases[request.Harness] = name
	}
	next, pkgs, e := resolver.Resolve(ctx, requests, old, updates)
	if e != nil {
		return nil, resolve.Lock{}, e
	}
	defer func() {
		for _, p := range pkgs {
			_ = p.Close()
		}
	}()
	// Preserve configured harness aliases for preflight rather than substituting
	// an unconfigured executable. Native resource layout is per provider.
	configs, e := config.Load(a.Home)
	if e != nil {
		return nil, resolve.Lock{}, e
	}
	contributions := []install.Contribution{}
	for i, p := range pkgs {
		h := next.Packages[i].Harness
		name := aliases[h]
		if name == "" {
			name = h
		}
		cfg, err := configs.Resolve(name)
		if err != nil {
			return nil, resolve.Lock{}, err
		}
		if cfg.Adapter != h {
			return nil, resolve.Lock{}, fmt.Errorf("configured harness %s no longer uses %s", name, h)
		}
		if harnessID != "" {
			selected, e := configs.Resolve(harnessID)
			if e != nil {
				return nil, resolve.Lock{}, e
			}
			if selected.Adapter == h {
				cfg = selected
			}
		}
		if scope == "user" {
			if e = checkUserEnvironment(cfg); e != nil {
				return nil, resolve.Lock{}, e
			}
		}
		if e = assets.CheckNativeVersion(ctx, cfg, root, p); e != nil {
			return nil, resolve.Lock{}, e
		}
		c, e := assets.RenderAt(root, p, h, scope)
		if e != nil {
			return nil, resolve.Lock{}, e
		}
		c.PackageID = resolve.Key(h, p.Manifest.ID)
		c.Metadata = map[string]string{"version": p.Manifest.Version, "kind": p.Manifest.Kind, "source": p.Source, "sha256": p.Digest, "harness": h}
		contributions = append(contributions, c)
	}
	next.Scope = scope
	declaration := resolve.Declaration{SchemaVersion: 1, Scope: scope, Packages: next.Roots}
	metadata := []install.File{{Path: ".atlas/packages.json", Data: resolve.Marshal(declaration), Mode: 0644}, {Path: ".atlas/packages.lock.json", Data: resolve.Marshal(next), Mode: 0644}}
	// Only local installation state is ignored; dependency declarations and locks
	// remain shareable when the project is checked into Git.
	ignorePath := filepath.Join(root, ".atlas", ".gitignore")
	ignore, err := os.ReadFile(ignorePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, resolve.Lock{}, err
	}
	privatePatterns := "install-receipt.json\ninstall-journal.json\ninstall.lock\n"
	if !strings.HasSuffix(string(ignore), privatePatterns) {
		if len(ignore) > 0 && ignore[len(ignore)-1] != '\n' {
			ignore = append(ignore, '\n')
		}
		ignore = append(ignore, []byte(privatePatterns)...)
		metadata = append(metadata, install.File{Path: ".atlas/.gitignore", Data: ignore, Mode: 0644})
	}
	if e = assets.ValidateContributions(contributions); e != nil {
		return nil, resolve.Lock{}, e
	}
	metadata = append(metadata, extraMetadata...)
	var plan *install.Transaction
	if len(base) > 0 {
		plan, e = install.PlanWithBase(root, base, contributions, metadata...)
	} else {
		plan, e = install.Plan(root, contributions, metadata...)
	}
	if e != nil {
		return nil, resolve.Lock{}, e
	}
	return plan, next, nil
}

func checkPackageScope(root, scope string, d resolve.Declaration, l resolve.Lock) error {
	for name, stored := range map[string]string{"packages.json": d.Scope, "packages.lock.json": l.Scope} {
		if _, err := os.Stat(filepath.Join(root, ".atlas", name)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if stored == "" {
			stored = "project"
		}
		if stored != scope {
			return fmt.Errorf("package state in %s belongs to %s scope; use --scope %s or a different project directory", root, stored, stored)
		}
	}
	return nil
}
