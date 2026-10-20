package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/install"
	"github.com/cottrellashley/atlas/internal/process"
)

// RenderNative writes documented native resources, so launching the provider
// directly loads exactly the same resources as an Atlas launch. It never edits
// a provider's internal package database or writes an implicit trust decision.
func RenderNative(pkg artifact.Package, provider, scope string) (install.Contribution, error) {
	if scope == "" {
		scope = "project"
	}
	c := install.Contribution{PackageID: pkg.Manifest.ID}
	n := pkg.Manifest.Native
	if n == nil {
		if strings.HasSuffix(pkg.Manifest.Kind, "-plugin") || pkg.Manifest.Kind == "pi-package" {
			return c, fmt.Errorf("%s requires native.path or native.source", pkg.Manifest.Kind)
		}
		return c, nil
	}
	want := map[string]string{"claude-plugin": "claude", "pi-package": "pi", "opencode-plugin": "opencode"}[pkg.Manifest.Kind]
	if want == "" || want != provider {
		return c, fmt.Errorf("native package kind %q does not support harness %q", pkg.Manifest.Kind, provider)
	}
	if (n.Path == "") == (n.Source == "") {
		return c, fmt.Errorf("native package requires exactly one of path or source")
	}
	layout, err := LayoutFor(provider, scope)
	if err != nil {
		return c, err
	}
	if n.Marketplace != "" {
		return c, fmt.Errorf("native.marketplace is not an install source: acquire the plugin directory as an Atlas Git release, or install the marketplace using the native Claude command")
	}
	if n.Source != "" {
		return nativeSource(pkg, provider, scope, layout)
	}
	root, err := nativeRoot(pkg.Root, n.Path)
	if err != nil {
		return c, err
	}
	if err := nativeDependencies(root, provider); err != nil {
		return c, err
	}
	name := nativeIdentity(pkg.Manifest.ID)
	var destination string
	switch provider {
	case "claude":
		if n.Target != "" {
			return c, fmt.Errorf("native.target applies only to OpenCode plugins")
		}
		var manifest struct {
			Name         string          `json:"name"`
			Version      string          `json:"version"`
			Dependencies json.RawMessage `json:"dependencies"`
			MCPServers   json.RawMessage `json:"mcpServers"`
			Experimental struct {
				Monitors json.RawMessage `json:"monitors"`
			} `json:"experimental"`
		}
		data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
		if err != nil {
			return c, fmt.Errorf("read Claude plugin manifest: %w", err)
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return c, fmt.Errorf("Claude plugin manifest: %w", err)
		}
		if !nativeName.MatchString(manifest.Name) {
			return c, fmt.Errorf("invalid Claude plugin name %q", manifest.Name)
		}
		if n.Name != "" && n.Name != manifest.Name {
			return c, fmt.Errorf("native.name %q differs from Claude manifest name %q", n.Name, manifest.Name)
		}
		if manifest.Version != "" && manifest.Version != pkg.Manifest.Version {
			return c, fmt.Errorf("Claude plugin version %q differs from Atlas release %q", manifest.Version, pkg.Manifest.Version)
		}
		if nonemptyJSON(manifest.Dependencies) {
			return c, fmt.Errorf("Claude skills-directory plugins cannot install marketplace dependencies: express dependencies as Atlas package dependencies and distribute each native plugin")
		}
		if scope == "project" && nonemptyJSON(manifest.Experimental.Monitors) {
			return c, fmt.Errorf("Claude does not load project skills-directory background monitors; install this plugin with --scope user")
		}
		if scope == "project" && claudeMCPBundle(manifest.MCPServers) {
			return c, fmt.Errorf("Claude does not load project skills-directory MCP bundles; declare inline or .mcp.json servers, or install with --scope user")
		}
		if len(n.Options) != 0 {
			return c, fmt.Errorf("Claude plugin options require native userConfig; use claude plugin configure after installation")
		}
		destination = filepath.Join(layout.Prefix, "skills", manifest.Name)
		c.Values = append(c.Values, install.Value{Path: layout.Settings, Pointer: "/enabledPlugins/" + nativePointer(manifest.Name+"@skills-dir"), Value: true})
	case "pi":
		if n.Target != "" {
			return c, fmt.Errorf("native.target applies only to OpenCode plugins")
		}
		if err := validatePiOptions(n.Options); err != nil {
			return c, err
		}
		destination = filepath.Join(layout.Prefix, "atlas-packages", name)
		source := "./atlas-packages/" + name
		var value any = source
		if len(n.Options) > 0 {
			value = nativeOptions("source", source, n.Options)
		}
		c.Values = append(c.Values, install.Value{Path: layout.Settings, Pointer: "/packages", Value: value, Array: true})
	case "opencode":
		server := nativeEntry(root, "server") || nativeEntry(root, "index")
		tui := nativeEntry(root, "tui")
		if !server && !tui {
			return c, fmt.Errorf("OpenCode local plugin needs server.js, index.js, or tui.js (or a corresponding .ts file) at its root")
		}
		if n.Target != "" && n.Target != "both" && n.Target != "server" && n.Target != "tui" {
			return c, fmt.Errorf("OpenCode native.target must be server, tui, or both")
		}
		if n.Target == "server" && !server || n.Target == "tui" && !tui || n.Target == "both" && (!server || !tui) {
			return c, fmt.Errorf("OpenCode plugin payload does not provide native.target=%s", n.Target)
		}
		if n.Target == "server" && tui || n.Target == "tui" && server {
			return c, fmt.Errorf("OpenCode local discovery loads all entrypoints in a plugin; native.target must describe the full payload (use both)")
		}
		// Both runtimes discover this same directory. Unlike server config, the
		// CLI has no project configuration file for TUI-only package sources.
		destination = filepath.Join(layout.Prefix, "plugins", name)
		if len(n.Options) > 0 {
			if tui {
				return c, fmt.Errorf("local OpenCode TUI plugin options are not project-scoped; use plugin defaults or a user-scoped native.source package")
			}
			rel, err := filepath.Rel(filepath.Dir(layout.Settings), destination)
			if err != nil {
				return c, err
			}
			c.Values = append(c.Values, install.Value{Path: layout.Settings, Pointer: "/plugins", Value: nativeOptions("package", "./"+filepath.ToSlash(rel), map[string]any{"options": n.Options}), Array: true})
		}
	}
	files, err := nativeFiles(root, destination)
	if err != nil {
		return c, err
	}
	c.Files = files
	return c, nil
}

var nativeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var exactNPM = regexp.MustCompile(`^(?:@[^/\s]+/)?[^@\s/]+@[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
var exactCommit = regexp.MustCompile(`^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$`)

func nativeSource(pkg artifact.Package, provider, scope string, layout Layout) (install.Contribution, error) {
	c := install.Contribution{PackageID: pkg.Manifest.ID}
	n := pkg.Manifest.Native
	if provider == "claude" {
		return c, fmt.Errorf("Claude native.source is unsupported: acquire the Git plugin into the Atlas release and select its directory with native.path")
	}
	if err := validateNativeSource(provider, n.Source); err != nil {
		return c, err
	}
	switch provider {
	case "pi":
		if n.Target != "" {
			return c, fmt.Errorf("native.target applies only to OpenCode plugins")
		}
		if err := validatePiOptions(n.Options); err != nil {
			return c, err
		}
		var value any = n.Source
		if len(n.Options) > 0 {
			value = nativeOptions("source", n.Source, n.Options)
		}
		c.Values = append(c.Values, install.Value{Path: layout.Settings, Pointer: "/packages", Value: value, Array: true})
	case "opencode":
		target := n.Target
		if target == "" {
			target = "server"
		}
		if target != "server" && target != "tui" && target != "both" {
			return c, fmt.Errorf("OpenCode native.target must be server, tui, or both")
		}
		if scope == "project" && target != "server" {
			return c, fmt.Errorf("OpenCode has no project CLI config for TUI package sources; distribute native.path with a tui.js entrypoint, or use --scope user")
		}
		var value any = n.Source
		if len(n.Options) > 0 {
			value = nativeOptions("package", n.Source, map[string]any{"options": n.Options})
		}
		if target == "server" || target == "both" {
			c.Values = append(c.Values, install.Value{Path: layout.Settings, Pointer: "/plugins", Value: value, Array: true})
		}
		if target == "tui" || target == "both" {
			c.Values = append(c.Values, install.Value{Path: filepath.Join(layout.Prefix, "cli.json"), Pointer: "/plugins", Value: value, Array: true})
		}
	}
	return c, nil
}

func validateNativeSource(provider, source string) error {
	// Native package sources are shared project configuration. Refuse embedded
	// URL credentials rather than persisting them in a declaration or receipt.
	for _, scheme := range []string{"https://", "http://", "ssh://"} {
		if at := strings.Index(source, scheme); at >= 0 {
			u, err := url.Parse(source[at:])
			if err != nil {
				return fmt.Errorf("invalid native source URL")
			}
			if u.User != nil {
				_, password := u.User.Password()
				if scheme != "ssh://" || password {
					return fmt.Errorf("native source must not contain URL credentials; configure native Git authentication separately")
				}
			}
		}
	}
	if provider == "pi" {
		if strings.HasPrefix(source, "npm:") && exactNPM.MatchString(strings.TrimPrefix(source, "npm:")) {
			return nil
		}
		if strings.HasPrefix(source, "git:") {
			idx := strings.LastIndex(source, "@")
			if idx > len("git:") && exactCommit.MatchString(source[idx+1:]) {
				return nil
			}
		}
	} else {
		if exactNPM.MatchString(source) {
			return nil
		}
		if idx := strings.LastIndex(source, "#"); idx > 0 && exactCommit.MatchString(source[idx+1:]) && (strings.HasPrefix(source, "git+") || strings.HasPrefix(source, "github:") || strings.HasPrefix(source, "https://")) {
			return nil
		}
	}
	return fmt.Errorf("native source %q must pin an exact npm version or full Git commit (Pi: npm:name@1.2.3 or git:host/repo@<commit>; OpenCode: name@1.2.3 or git+https://host/repo#<commit>)", source)
}

func validatePiOptions(options map[string]any) error {
	for key, value := range options {
		switch key {
		case "extensions", "skills", "prompts", "themes":
			switch list := value.(type) {
			case []string:
			case []any:
				for _, entry := range list {
					if _, ok := entry.(string); !ok {
						return fmt.Errorf("Pi native.options.%s must be an array of strings", key)
					}
				}
			default:
				return fmt.Errorf("Pi native.options.%s must be an array of strings", key)
			}
		case "autoload":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("Pi native.options.autoload must be boolean")
			}
		default:
			return fmt.Errorf("unsupported Pi package option %q", key)
		}
	}
	return nil
}

func nativeOptions(key, source string, options map[string]any) map[string]any {
	out := make(map[string]any, len(options)+1)
	for k, v := range options {
		out[k] = v
	}
	out[key] = source
	return out
}

func nativeIdentity(id string) string {
	hash := sha256.Sum256([]byte(id))
	return "atlas-" + hex.EncodeToString(hash[:8])
}
func nativePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func nonemptyJSON(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s != "" && s != "null" && s != "{}" && s != "[]"
}
func claudeMCPBundle(v json.RawMessage) bool {
	var value any
	if json.Unmarshal(v, &value) != nil {
		return false
	}
	var check func(any) bool
	check = func(x any) bool {
		switch x := x.(type) {
		case string:
			return strings.HasSuffix(x, ".mcpb") || strings.HasSuffix(x, ".dxt") || strings.HasPrefix(x, "https://")
		case []any:
			for _, v := range x {
				if check(v) {
					return true
				}
			}
		}
		return false
	}
	return check(value)
}
func nativeEntry(root, name string) bool {
	for _, ext := range []string{".js", ".ts"} {
		if info, err := os.Stat(filepath.Join(root, name+ext)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func nativeRoot(root, path string) (string, error) {
	if path == "." {
		path = ""
	}
	if filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return "", fmt.Errorf("native.path must be relative to the artifact")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("native.path escapes artifact")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, clean)
	for p := target; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("native.path traverses a symlink: %s", p)
		}
		if p == base {
			break
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("native.path must select a directory")
	}
	return target, nil
}

func nativeFiles(root, destination string) ([]install.File, error) {
	var files []install.File
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("native package contains symlink: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("native package contains non-regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		files = append(files, install.File{Path: filepath.Join(destination, rel), Data: data, Mode: mode})
		return nil
	})
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("native package payload is empty")
	}
	return files, err
}

// Local provider packages are loaded in place. Neither Pi nor Claude installs
// their dependencies there, so accepting unresolved imports would create a
// successful Atlas installation which fails at native startup.
func nativeDependencies(root, provider string) error {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
		Optional     map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("native package.json: %w", err)
	}
	for name := range manifest.Dependencies {
		if provider == "pi" && (strings.HasPrefix(name, "@earendil-works/pi-") || name == "typebox") {
			return fmt.Errorf("Pi host package %s must be a peerDependency, not bundled under dependencies", name)
		}
		if strings.Contains(name, "..") || strings.ContainsAny(name, "\\") {
			return fmt.Errorf("invalid dependency name %q", name)
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules", filepath.FromSlash(name), "package.json")); err != nil {
			return fmt.Errorf("local native package dependency %q is not included: bundle runtime imports before publishing or use a pinned native.source (native providers do not install dependencies of local payloads)", name)
		}
	}
	return nil
}

// CheckNativeVersion is a read-only preflight for features whose layouts changed
// across provider versions. These are the minimum versions exercised by Atlas.
func CheckNativeVersion(ctx context.Context, cfg harness.Config, cwd string, pkg artifact.Package) error {
	// Pi added native MCP configuration in the tested 1.3 line. Older Pi
	// executables otherwise silently ignore a successfully installed mcp.json.
	needsPiMCP := cfg.Adapter == "pi" && pkg.Manifest.MCP != ""
	if pkg.Manifest.Native == nil && !needsPiMCP {
		return nil
	}
	feature := "native packages"
	if needsPiMCP && pkg.Manifest.Native == nil {
		feature = "native MCP servers"
	}
	minimum := map[string][3]int{"claude": {2, 1, 302}, "pi": {1, 3, 0}, "opencode": {2, 1, 0}}
	want, ok := minimum[cfg.Adapter]
	if !ok {
		return fmt.Errorf("unsupported native package harness %q", cfg.Adapter)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := process.Command(ctx, cfg, cwd, "--version").Output()
	if err != nil {
		return fmt.Errorf("check %s version: %w", cfg.Adapter, err)
	}
	match := regexp.MustCompile(`(?:^|[^0-9])([0-9]+)\.([0-9]+)\.([0-9]+)`).FindStringSubmatch(string(output))
	if len(match) != 4 {
		return fmt.Errorf("cannot read %s version", cfg.Adapter)
	}
	var got [3]int
	for i := range got {
		got[i], _ = strconv.Atoi(match[i+1])
	}
	if got[0] != want[0] {
		return fmt.Errorf("%s %s support major version %d; found %d", cfg.Adapter, feature, want[0], got[0])
	}
	validate := func() error {
		if cfg.Adapter != "claude" || pkg.Manifest.Native == nil || pkg.Manifest.Native.Path == "" {
			return nil
		}
		root, err := nativeRoot(pkg.Root, pkg.Manifest.Native.Path)
		if err != nil {
			return err
		}
		out, err := process.Command(ctx, cfg, cwd, "plugin", "validate", root).CombinedOutput()
		if err != nil {
			return fmt.Errorf("Claude native plugin validation failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	for i := range got {
		if got[i] > want[i] {
			return validate()
		}
		if got[i] < want[i] {
			return fmt.Errorf("%s %s require version %d.%d.%d or newer (found %d.%d.%d)", cfg.Adapter, feature, want[0], want[1], want[2], got[0], got[1], got[2])
		}
	}
	return validate()
}
