// Package assets renders distribution packages into provider-native resources.
// Rendering is pure: it never starts a provider or writes to the target project.
package assets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/install"
	"gopkg.in/yaml.v3"
)

// Layout paths are relative to the project root or, for user scope, HOME.
// Callers must handle nondefault provider config-directory environment variables
// before selecting a root. Project installations preserve native trust prompts.
type Layout struct {
	Prefix     string
	Settings   string
	MCP        string
	MCPPointer string
}

func LayoutFor(harness, scope string) (Layout, error) {
	if scope == "" {
		scope = "project"
	}
	if scope != "project" && scope != "user" {
		return Layout{}, fmt.Errorf("unsupported package scope %q", scope)
	}
	user := scope == "user"
	switch harness {
	case "claude":
		mcp := ".mcp.json"
		if user {
			mcp = ".claude.json"
		}
		return Layout{".claude", ".claude/settings.json", mcp, "/mcpServers"}, nil
	case "pi":
		prefix := ".pi"
		if user {
			prefix = ".pi/agent"
		}
		return Layout{prefix, prefix + "/settings.json", prefix + "/mcp.json", "/mcpServers"}, nil
	case "opencode":
		prefix, settings := ".opencode", "opencode.json"
		if user {
			prefix, settings = ".config/opencode", ".config/opencode/opencode.json"
		}
		return Layout{prefix, settings, settings, "/mcp"}, nil
	default:
		return Layout{}, fmt.Errorf("unsupported package harness %q", harness)
	}
}

// Render returns one package's complete desired resources. Native formats are
// also usable when users launch the provider directly without Atlas.
func Render(pkg artifact.Package, harness, scope string) (install.Contribution, error) {
	out := install.Contribution{PackageID: pkg.Manifest.ID}
	layout, err := LayoutFor(harness, scope)
	if err != nil {
		return out, err
	}
	if len(pkg.Manifest.Harnesses) > 0 {
		found := false
		for _, h := range pkg.Manifest.Harnesses {
			found = found || h == harness
		}
		if !found {
			return out, fmt.Errorf("package %s does not support %s", pkg.Manifest.ID, harness)
		}
	}
	for _, path := range pkg.Manifest.Skills {
		skill, err := ReadSkill(pkg.Root, path)
		if err != nil {
			return out, err
		}
		files, err := copyTree(pkg.Root, path, filepath.Join(layout.Prefix, "skills", skill.Name))
		if err != nil {
			return out, err
		}
		out.Files = append(out.Files, files...)
	}
	if pkg.Manifest.MCP != "" {
		servers, err := readServers(pkg.Root, pkg.Manifest.MCP)
		if err != nil {
			return out, err
		}
		for _, name := range sortedKeys(servers) {
			entry, err := renderServer(name, servers[name], harness)
			if err != nil {
				return out, err
			}
			out.Values = append(out.Values, ObjectValues(layout.MCP, layout.MCPPointer+"/"+escapePointer(name), entry)...)
		}
	}
	for _, provider := range []string{"*", harness} {
		if path := pkg.Manifest.Config[provider]; path != "" {
			object, err := readObject(pkg.Root, path)
			if err != nil {
				return out, err
			}
			out.Values = append(out.Values, ObjectValues(layout.Settings, "", object)...)
		}
		for _, target := range sortedKeys(pkg.Manifest.Files[provider]) {
			if !allowedTarget(target, harness, scope, layout) {
				return out, fmt.Errorf("package file target %q is outside the %s native resource paths", target, harness)
			}
			source := pkg.Manifest.Files[provider][target]
			if nativeJSON(target, layout) {
				object, err := readObject(pkg.Root, source)
				if err != nil {
					return out, err
				}
				out.Values = append(out.Values, ObjectValues(target, "", object)...)
			} else {
				files, err := copyTree(pkg.Root, source, target)
				if err != nil {
					return out, err
				}
				out.Files = append(out.Files, files...)
			}
		}
	}
	native, err := RenderNative(pkg, harness, scope)
	if err != nil {
		return out, err
	}
	out.Files = append(out.Files, native.Files...)
	out.Values = append(out.Values, native.Values...)
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	sort.SliceStable(out.Values, func(i, j int) bool {
		if out.Values[i].Path != out.Values[j].Path {
			return out.Values[i].Path < out.Values[j].Path
		}
		return out.Values[i].Pointer < out.Values[j].Pointer
	})
	return out, nil
}

// RenderAt selects an existing native JSONC configuration rather than creating
// an overriding JSON file. The install engine preserves its comments. Reading
// either file here does not authorize changing its contents; Plan checks them.
func RenderAt(root string, pkg artifact.Package, harness, scope string) (install.Contribution, error) {
	out, err := Render(pkg, harness, scope)
	if err != nil || harness != "opencode" {
		return out, err
	}
	layout, _ := LayoutFor(harness, scope)
	// OpenCode 2 CLI settings use exactly the user-level cli.json filename.
	// That file accepts comments, but neither cli.jsonc nor legacy tui.json
	// are current configuration sources. Only server config has a .jsonc name.
	for _, path := range []string{layout.Settings} {
		alternative := strings.TrimSuffix(path, ".json") + ".jsonc"
		_, jsonErr := os.Lstat(filepath.Join(root, path))
		_, jsoncErr := os.Lstat(filepath.Join(root, alternative))
		if jsonErr != nil && !os.IsNotExist(jsonErr) {
			return out, jsonErr
		}
		if jsoncErr != nil && !os.IsNotExist(jsoncErr) {
			return out, jsoncErr
		}
		if jsonErr == nil && jsoncErr == nil {
			return out, fmt.Errorf("both %s and %s exist; consolidate the native configuration before installing packages", path, alternative)
		}
		if jsoncErr == nil {
			for i := range out.Values {
				if out.Values[i].Path == path {
					out.Values[i].Path = alternative
				}
			}
		}
	}
	return out, nil
}

// ValidateContributions checks identities that a provider normalizes beyond
// their JSON spelling. Call it on the complete desired package set before Plan.
func ValidateContributions(contributions []install.Contribution) error {
	names := map[string]string{}
	claudeSkills := map[string]string{}
	claudePlugins := map[string]string{}
	for _, contribution := range contributions {
		for _, file := range contribution.Files {
			parts := strings.Split(filepath.ToSlash(filepath.Clean(file.Path)), "/")
			if len(parts) < 4 || parts[0] != ".claude" || parts[1] != "skills" {
				continue
			}
			root := strings.Join(parts[:3], "/")
			if len(parts) == 4 && parts[3] == "SKILL.md" {
				claudeSkills[root] = contribution.PackageID
			} else if len(parts) == 5 && parts[3] == ".claude-plugin" && parts[4] == "plugin.json" {
				claudePlugins[root] = contribution.PackageID
			}
			if skillOwner, skill := claudeSkills[root]; skill {
				if pluginOwner, plugin := claudePlugins[root]; plugin {
					return fmt.Errorf("Claude resource directory %s is both a standalone skill (%s) and a native plugin (%s); use distinct skill and plugin names", root, skillOwner, pluginOwner)
				}
			}
		}
		for _, value := range contribution.Values {
			parts := strings.Split(value.Pointer, "/")
			if len(parts) < 3 || parts[1] != "mcp" && parts[1] != "mcpServers" {
				continue
			}
			name := strings.ReplaceAll(strings.ReplaceAll(parts[2], "~1", "/"), "~0", "~")
			if !serverName.MatchString(name) {
				return fmt.Errorf("MCP server %q in %s must use letters, digits, underscores or hyphens", name, contribution.PackageID)
			}
			key := value.Path + "/" + parts[1] + "/" + strings.ReplaceAll(name, "-", "_")
			if prior, exists := names[key]; exists && prior != name {
				return fmt.Errorf("MCP names %q and %q in %s collide after native normalization", prior, name, value.Path)
			}
			names[key] = name
		}
	}
	return nil
}

// ObjectValues recursively owns individual configuration keys. Arrays remain
// intact unless they are resource lists where entries have independent identity.
// Native installers may explicitly use Array=true for such lists.
func ObjectValues(path, pointer string, object map[string]any) []install.Value {
	var out []install.Value
	for _, key := range sortedKeys(object) {
		p := pointer + "/" + escapePointer(key)
		value := object[key]
		if nested, ok := value.(map[string]any); ok && len(nested) > 0 {
			out = append(out, ObjectValues(path, p, nested)...)
		} else {
			out = append(out, install.Value{Path: filepath.ToSlash(path), Pointer: p, Value: value})
		}
	}
	return out
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func safeSource(root, path string) (string, error) {
	clean := filepath.Clean(path)
	if path == "" || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe package source path %q", path)
	}
	current := filepath.Clean(root)
	parts := append([]string{"."}, strings.Split(clean, string(filepath.Separator))...)
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("package symlinks are not supported: %s", path)
		}
	}
	return current, nil
}

func readFile(root, path string) ([]byte, error) {
	full, err := safeSource(root, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("package file must be regular and at most 16 MiB: %s", path)
	}
	return os.ReadFile(full)
}

func readObject(root, path string) (map[string]any, error) {
	data, err := readFile(root, path)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("package configuration %s must be a JSON object", path)
	}
	var extra any
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("package configuration %s contains trailing JSON", path)
	}
	return object, nil
}

func copyTree(root, from, to string) ([]install.File, error) {
	base, err := safeSource(root, from)
	if err != nil {
		return nil, err
	}
	var files []install.File
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package symlink is not supported: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("package entry is not a regular file: %s", path)
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 16<<20 {
			return fmt.Errorf("package file exceeds 16 MiB: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, install.File{Path: filepath.ToSlash(filepath.Join(to, rel)), Data: data, Mode: info.Mode().Perm()})
		return nil
	})
	return files, err
}

func allowedTarget(path, harness, scope string, layout Layout) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean != path || filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	if harness == "opencode" {
		if clean == layout.Prefix+"/tui.json" || clean == layout.Prefix+"/tui.jsonc" || clean == layout.Prefix+"/cli.jsonc" || scope != "user" && clean == layout.Prefix+"/cli.json" {
			return false
		}
	}
	if strings.HasPrefix(clean, layout.Prefix+"/") || clean == layout.Settings || clean == layout.MCP {
		return true
	}
	if scope == "user" {
		return false
	}
	return clean == "AGENTS.md" || harness == "claude" && clean == "CLAUDE.md" || harness == "opencode" && clean == "opencode.jsonc"
}

func nativeJSON(path string, layout Layout) bool {
	return path == layout.Settings || path == layout.MCP || path == strings.TrimSuffix(layout.Settings, ".json")+".jsonc" || path == layout.Prefix+"/settings.local.json" || path == layout.Prefix+"/models.json" || path == ".config/opencode/cli.json"
}

// SkillMetadata is the portable Agent Skills identity. The complete SKILL.md is
// preserved; Atlas reads only the required scalar fields rather than rewriting
// provider-specific frontmatter.
type SkillMetadata struct {
	Name        string
	Description string
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func ReadSkill(root, path string) (SkillMetadata, error) {
	var result SkillMetadata
	data, err := readFile(root, filepath.Join(path, "SKILL.md"))
	if err != nil {
		return result, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) != "---" {
		return result, fmt.Errorf("skill %s requires YAML frontmatter", path)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return result, fmt.Errorf("skill %s has unterminated frontmatter", path)
	}
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return result, fmt.Errorf("skill %s frontmatter: %w", path, err)
	}
	name, nameOK := metadata["name"].(string)
	description, descriptionOK := metadata["description"].(string)
	if !nameOK || !descriptionOK {
		return result, fmt.Errorf("skill %s requires string name and description", path)
	}
	result = SkillMetadata{name, description}
	if len(result.Name) > 64 || !skillName.MatchString(result.Name) {
		return result, fmt.Errorf("skill %s name must contain 1–64 lowercase letters/digits separated by single hyphens", path)
	}
	if filepath.Clean(path) != "." && filepath.Base(filepath.Clean(path)) != result.Name {
		return result, fmt.Errorf("skill %s name %q must match its directory", path, result.Name)
	}
	if n := len([]rune(result.Description)); n == 0 || n > 1024 {
		return result, fmt.Errorf("skill %s description must contain 1–1024 characters", path)
	}
	return result, nil
}

// MCPServer is the portable transport subset. Native carries explicit provider
// extensions (for example Pi exposure) without pretending they are portable.
type MCPServer struct {
	Transport string                    `json:"transport,omitempty"`
	Command   string                    `json:"command,omitempty"`
	Args      []string                  `json:"args,omitempty"`
	URL       string                    `json:"url,omitempty"`
	Env       map[string]string         `json:"env,omitempty"`
	Headers   map[string]string         `json:"headers,omitempty"`
	Native    map[string]map[string]any `json:"native,omitempty"`
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func readServers(root, path string) (map[string]MCPServer, error) {
	data, err := readFile(root, path)
	if err != nil {
		return nil, err
	}
	var result map[string]MCPServer
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&result); err != nil || result == nil {
		return nil, fmt.Errorf("MCP package %s must be a server map: %v", path, err)
	}
	var extra any
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("MCP package %s contains trailing JSON", path)
	}
	canonical := map[string]string{}
	for name := range result {
		if !serverName.MatchString(name) {
			return nil, fmt.Errorf("MCP server %q must use letters, digits, underscores or hyphens", name)
		}
		id := strings.ReplaceAll(name, "-", "_")
		if prior, ok := canonical[id]; ok {
			return nil, fmt.Errorf("MCP names %q and %q collide after native normalization", prior, name)
		}
		canonical[id] = name
	}
	return result, nil
}

func renderServer(name string, s MCPServer, harness string) (map[string]any, error) {
	if s.Transport == "" {
		s.Transport = "stdio"
		if s.URL != "" {
			s.Transport = "http"
		}
	}
	convert := func(v string) string {
		if harness == "opencode" {
			return envReference.ReplaceAllString(v, "{env:$1}")
		}
		return v
	}
	convertMap := func(m map[string]string) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = convert(v)
		}
		return out
	}
	entry := map[string]any{}
	switch s.Transport {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" || s.URL != "" || len(s.Headers) != 0 {
			return nil, fmt.Errorf("MCP %s stdio requires command and cannot contain url/headers", name)
		}
		args := make([]string, len(s.Args))
		for i, value := range s.Args {
			args[i] = convert(value)
		}
		if harness == "opencode" {
			entry = map[string]any{"type": "local", "command": append([]string{convert(s.Command)}, args...), "enabled": true}
			if len(s.Env) > 0 {
				entry["environment"] = convertMap(s.Env)
			}
		} else {
			entry = map[string]any{"command": s.Command}
			if len(args) > 0 {
				entry["args"] = args
			}
			if len(s.Env) > 0 {
				entry["env"] = convertMap(s.Env)
			}
		}
	case "http":
		if harness == "pi" && envReference.MatchString(s.URL) {
			return nil, fmt.Errorf("MCP %s: Pi 1.3 does not expand environment references in URL; use a literal HTTP(S) endpoint (env/header references are supported)", name)
		}
		// Providers expand ${NAME} (translated to {env:NAME} for OpenCode).
		// A complete endpoint can come from the user's environment; its actual
		// scheme is then checked by the native client when connecting.
		wholeReference := envReference.FindString(s.URL) == s.URL && s.URL != ""
		u, err := url.Parse(envReference.ReplaceAllString(s.URL, "1"))
		validURL := wholeReference || err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil
		if !validURL || s.Command != "" || len(s.Args) != 0 || len(s.Env) != 0 {
			return nil, fmt.Errorf("MCP %s http requires an HTTP(S) URL without credentials and cannot contain command/args/env", name)
		}
		entry = map[string]any{"type": "http", "url": convert(s.URL)}
		if harness == "opencode" {
			entry["type"], entry["enabled"] = "remote", true
		}
		if len(s.Headers) > 0 {
			entry["headers"] = convertMap(s.Headers)
		}
	default:
		return nil, fmt.Errorf("MCP %s has unsupported transport %q; use stdio or http", name, s.Transport)
	}
	for provider, overlay := range s.Native {
		if provider != "claude" && provider != "pi" && provider != "opencode" {
			return nil, fmt.Errorf("MCP %s has unknown native harness %q", name, provider)
		}
		if provider == harness {
			for key, value := range overlay {
				switch key {
				case "type", "command", "args", "url", "env", "environment", "headers":
					return nil, fmt.Errorf("MCP %s native option %q duplicates a portable field", name, key)
				}
				if key == "enabled" {
					if _, ok := value.(bool); !ok {
						return nil, fmt.Errorf("MCP %s native enabled option must be boolean", name)
					}
				}
				entry[key] = value
			}
		}
	}
	return entry, nil
}
