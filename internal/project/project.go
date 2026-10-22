// Package project renders portable templates into native harness resources.
// Initialization plans all files before writing and refuses conflicting edits.
package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Agent struct {
	Model          string `json:"model,omitempty"`
	Thinking       string `json:"thinking,omitempty"`
	NativeAgent    string `json:"nativeAgent,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
}
type Project struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Harness         string            `json:"harness"`
	Adapter         string            `json:"adapter"`
	Template        string            `json:"template"`
	TemplateVersion string            `json:"templateVersion"`
	DefaultAgent    string            `json:"defaultAgent"`
	Agents          map[string]Agent  `json:"agents"`
	Files           map[string]string `json:"files"`
}
type Manifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	ID            string           `json:"id"`
	Version       string           `json:"version"`
	Instructions  string           `json:"instructions"`
	Skills        []string         `json:"skills"`
	MCP           string           `json:"mcp"`
	Requires      []string         `json:"requires"`
	Agents        map[string]Agent `json:"agents,omitempty"`
	Harnesses     map[string]struct {
		Overlay string `json:"overlay"`
	} `json:"harnesses"`
}
type MCPServer struct {
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	URL       string            `json:"url,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}
type File struct {
	Path    string      `json:"path"`
	Action  string      `json:"action"`
	Data    []byte      `json:"-"`
	Old     []byte      `json:"-"`
	Existed bool        `json:"-"`
	Mode    fs.FileMode `json:"-"`
	OldMode fs.FileMode `json:"-"`
}
type Plan struct {
	Project Project `json:"project"`
	Files   []File  `json:"files"`
}

func Load(cwd string) (*Project, string, error) {
	dir, e := filepath.Abs(cwd)
	if e != nil {
		return nil, "", e
	}
	for {
		b, e := os.ReadFile(filepath.Join(dir, ".atlas", "project.json"))
		if e == nil {
			var p Project
			e = json.Unmarshal(b, &p)
			if e != nil {
				return nil, "", e
			}
			if p.SchemaVersion != 1 {
				return nil, "", errors.New("unsupported project schemaVersion")
			}
			return &p, dir, nil
		}
		if !os.IsNotExist(e) {
			return nil, "", e
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", nil
		}
		dir = parent
	}
}

func safe(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(rel) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe template path %q", rel)
	}
	p := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink in template path %s", p)
		}
	}
	return filepath.Join(root, clean), nil
}
func read(root, path string) ([]byte, error) {
	p, e := safe(root, path)
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, e
	}
	if len(b) > 16<<20 {
		return nil, fmt.Errorf("template file too large: %s", path)
	}
	return b, nil
}
func encode(v any) []byte  { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

const ResearcherVersion = "1.1.0"

const researcherInstructions = `# Researcher

You are the project's researcher. Establish the question and constraints, inspect
the available evidence, and produce a concise answer with sources. Distinguish
observations from assumptions. Prefer authoritative primary sources. Report
uncertainty and contradictions. Never invent citations or claim unrun tests pass.

For substantial research, keep a reproducible report in research/ with the
question, findings, source links, and open questions. Ask before destructive
actions. Keep credentials out of reports and version control.

For requested setup changes, use atlas help agent. Atlas can add skills, MCP
servers, and shared packages to this project's native configuration. Preview
changes with --dry-run --json and preserve existing user edits. Use --no-open
when initializing another project from a running agent. Conversations remain
in the native harness; atlas session list browses their history.
`
const researchSkill = `---
name: research
description: Investigate a question using evidence and write a reproducible research report.
---

1. State the question, scope and success criteria.
2. Inspect the project's files and consult authoritative primary sources.
3. Compare evidence; distinguish verified facts, inference and unresolved claims.
4. Write findings with source links and practical next steps.
5. Record what was actually tested and any remaining limitations.
`

func Build(root, home, harnessID, adapter, template string) (Plan, error) {
	var plan Plan
	root, e := filepath.Abs(root)
	if e != nil {
		return plan, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return plan, e
	}
	outputs := map[string][]byte{}
	m := Manifest{}
	modes := map[string]fs.FileMode{}
	servers := map[string]MCPServer{}
	prefix := map[string]string{"claude": ".claude", "pi": ".pi", "omp": ".omp", "opencode": ".opencode"}[adapter]
	if prefix == "" {
		return plan, fmt.Errorf("unsupported template adapter %q", adapter)
	}
	if template == "researcher" {
		m = Manifest{SchemaVersion: 1, ID: "researcher", Version: ResearcherVersion, Agents: map[string]Agent{"researcher": {}}}
		outputs["AGENTS.md"] = []byte(researcherInstructions)
		outputs[prefix+"/skills/research/SKILL.md"] = []byte(researchSkill)
	} else {
		source := template
		if !filepath.IsAbs(source) {
			if info, err := os.Stat(source); err != nil || !info.IsDir() {
				source = filepath.Join(home, "templates", template)
			}
		}
		source, e = filepath.Abs(source)
		if e != nil {
			return plan, e
		}
		source, e = filepath.EvalSymlinks(source)
		if e != nil {
			return plan, e
		}
		b, err := read(source, "template.json")
		if err != nil {
			return plan, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			return plan, err
		}
		if m.SchemaVersion != 1 || m.ID == "" || m.Version == "" {
			return plan, errors.New("template requires schemaVersion 1, id and version")
		}
		for _, cap := range m.Requires {
			switch cap {
			case "skills", "mcp.stdio", "mcp.http":
			default:
				return plan, fmt.Errorf("unsupported required template capability %q", cap)
			}
		}
		if m.Instructions != "" {
			b, err = read(source, m.Instructions)
			if err != nil {
				return plan, err
			}
			outputs["AGENTS.md"] = b
		}
		copyTree := func(from, to string) error {
			base, err := safe(source, from)
			if err != nil {
				return err
			}
			return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("template symlink not allowed: %s", path)
				}
				if d.IsDir() {
					return nil
				}
				if !d.Type().IsRegular() {
					return fmt.Errorf("template entry is not a regular file: %s", path)
				}
				rel, err := filepath.Rel(base, path)
				if err != nil {
					return err
				}
				target := filepath.Join(to, rel)
				if target == ".atlas" || strings.HasPrefix(target, ".atlas/") {
					return errors.New("overlays may not write .atlas metadata")
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if len(b) > 16<<20 {
					return errors.New("template file too large")
				}
				if _, ok := outputs[target]; ok {
					return fmt.Errorf("duplicate template output %s", target)
				}
				outputs[target] = b
				info, err := d.Info()
				if err != nil {
					return err
				}
				modes[target] = info.Mode().Perm()
				return nil
			})
		}
		for _, skill := range m.Skills {
			if _, err = read(source, filepath.Join(skill, "SKILL.md")); err != nil {
				return plan, err
			}
			if err = copyTree(skill, filepath.Join(prefix, "skills", filepath.Base(skill))); err != nil {
				return plan, err
			}
		}
		if m.MCP != "" {
			b, err = read(source, m.MCP)
			if err != nil {
				return plan, err
			}
			if err = json.Unmarshal(b, &servers); err != nil {
				return plan, fmt.Errorf("MCP: %w", err)
			}
		}
		if overlay := m.Harnesses[adapter].Overlay; overlay != "" {
			if err = copyTree(overlay, ""); err != nil {
				return plan, err
			}
		}
	}
	if _, ok := outputs["AGENTS.md"]; adapter == "claude" && ok {
		if _, present := outputs["CLAUDE.md"]; !present {
			outputs["CLAUDE.md"] = []byte("@AGENTS.md\n")
		}
	}
	nativeMCP := map[string]any{}
	for name, s := range servers {
		if name == "" {
			return plan, errors.New("empty MCP server name")
		}
		if s.Transport == "" {
			s.Transport = "stdio"
		}
		if adapter == "opencode" {
			s.Command = envRefs(s.Command)
			s.URL = envRefs(s.URL)
			for i, v := range s.Args {
				s.Args[i] = envRefs(v)
			}
			for k, v := range s.Env {
				s.Env[k] = envRefs(v)
			}
			for k, v := range s.Headers {
				s.Headers[k] = envRefs(v)
			}
		}
		entry := map[string]any{}
		switch s.Transport {
		case "stdio":
			if s.Command == "" {
				return plan, fmt.Errorf("MCP %s requires command", name)
			}
			if adapter == "opencode" {
				entry = map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
				if len(s.Env) > 0 {
					entry["environment"] = s.Env
				}
			} else {
				entry = map[string]any{"command": s.Command}
				if len(s.Args) > 0 {
					entry["args"] = s.Args
				}
				if len(s.Env) > 0 {
					entry["env"] = s.Env
				}
			}
		case "http":
			if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
				return plan, fmt.Errorf("MCP %s requires HTTP(S) URL", name)
			}
			if adapter == "opencode" {
				entry = map[string]any{"type": "remote", "url": s.URL, "enabled": true}
			} else {
				entry = map[string]any{"type": "http", "url": s.URL}
			}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
		default:
			return plan, fmt.Errorf("unsupported MCP transport %q", s.Transport)
		}
		nativeMCP[name] = entry
	}
	native := map[string]map[string]any{}
	switch adapter {
	case "opencode":
		native["opencode.json"] = map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": nativeMCP}
	case "claude":
		native[".claude/settings.json"] = map[string]any{}
		native[".mcp.json"] = map[string]any{"mcpServers": nativeMCP}
	case "pi":
		native[".pi/settings.json"] = map[string]any{}
		native[".pi/mcp.json"] = map[string]any{"mcpServers": nativeMCP}
	case "omp":
		// OMP owns config.yml. An empty template has no settings to contribute;
		// leave existing YAML, comments, and policy choices untouched.
		native[".omp/mcp.json"] = map[string]any{"mcpServers": nativeMCP}
	}
	for path, v := range native {
		if b, ok := outputs[path]; ok {
			var existing map[string]any
			if e = json.Unmarshal(b, &existing); e != nil || existing == nil {
				return plan, fmt.Errorf("overlay %s must be a JSON object", path)
			}
			if e = merge(existing, v, path); e != nil {
				return plan, e
			}
			v = existing
		}
		outputs[path] = encode(v)
	}
	p := Project{SchemaVersion: 1, Harness: harnessID, Adapter: adapter, Template: m.ID, TemplateVersion: m.Version, DefaultAgent: m.ID, Agents: m.Agents, Files: map[string]string{}}
	if len(p.Agents) == 0 {
		p.Agents = map[string]Agent{m.ID: {}}
	}
	if _, ok := p.Agents[p.DefaultAgent]; !ok {
		names := []string{}
		for name := range p.Agents {
			names = append(names, name)
		}
		sort.Strings(names)
		p.DefaultAgent = names[0]
	}
	previous := Project{}
	if b, err := os.ReadFile(filepath.Join(root, ".atlas", "project.json")); err == nil {
		if e = json.Unmarshal(b, &previous); e != nil {
			return plan, e
		}
		if previous.Harness != harnessID || previous.Template != m.ID {
			return plan, errors.New("project already initialized with a different harness/template; use a separate project directory")
		}
		p.Agents = previous.Agents
		p.DefaultAgent = previous.DefaultAgent
	}
	keys := []string{}
	for path := range outputs {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	// Avoid shadowing an existing JSONC config with a new opencode.json.
	if adapter == "opencode" {
		if _, err := os.Stat(filepath.Join(root, "opencode.jsonc")); err == nil {
			if _, err = os.Stat(filepath.Join(root, "opencode.json")); os.IsNotExist(err) {
				return plan, errors.New("existing opencode.jsonc: merge the template configuration explicitly before initialization; Atlas will not shadow or rewrite comments")
			}
		}
	}
	for _, path := range keys {
		full, err := safe(root, path)
		if err != nil {
			return plan, err
		}
		b := outputs[path]
		old, err := os.ReadFile(full)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return plan, err
		}
		if exists && !bytes.Equal(old, b) {
			if _, isJSON := native[path]; isJSON {
				var existing, incoming map[string]any
				if json.Unmarshal(old, &existing) != nil || existing == nil {
					return plan, fmt.Errorf("existing %s is not a JSON object", path)
				}
				_ = json.Unmarshal(b, &incoming)
				if err = merge(existing, incoming, path); err != nil {
					return plan, err
				}
				merged := encode(existing)
				var oldValue any
				_ = json.Unmarshal(old, &oldValue)
				if bytes.Equal(encode(oldValue), merged) {
					b = old
				} else {
					b = merged
				}
			} else if previous.Files[path] == "" || hash(old) != previous.Files[path] {
				return plan, fmt.Errorf("conflicting existing file %s (left unchanged)", path)
			}
		}
		action := "create"
		if exists {
			action = "update"
		}
		if bytes.Equal(old, b) {
			action = "unchanged"
		}
		mode := modes[path]
		if mode == 0 {
			mode = 0644
		}
		var oldMode fs.FileMode
		if exists {
			info, err := os.Stat(full)
			if err != nil {
				return plan, err
			}
			oldMode = info.Mode().Perm()
			mode = oldMode
		}
		plan.Files = append(plan.Files, File{Path: path, Action: action, Data: b, Old: old, Existed: exists, Mode: mode, OldMode: oldMode})
		p.Files[path] = hash(b)
	}
	meta := encode(p)
	path := ".atlas/project.json"
	full, e := safe(root, path)
	if e != nil {
		return plan, e
	}
	old, e := os.ReadFile(full)
	if e != nil && !os.IsNotExist(e) {
		return plan, e
	}
	action := "create"
	if e == nil {
		action = "update"
	}
	if bytes.Equal(meta, old) {
		action = "unchanged"
	}
	mode := fs.FileMode(0600)
	if e == nil {
		info, err := os.Stat(full)
		if err != nil {
			return plan, err
		}
		mode = info.Mode().Perm()
	}
	plan.Files = append(plan.Files, File{Path: path, Action: action, Data: meta, Old: old, Existed: e == nil, Mode: mode, OldMode: mode})
	plan.Project = p
	return plan, nil
}

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func envRefs(value string) string { return environmentReference.ReplaceAllString(value, "{env:${1}}") }

// merge is additive and refuses incompatible scalar values; it preserves
// unrelated native configuration and never changes a user's selected model.
func merge(dst, src map[string]any, path string) error {
	for k, v := range src {
		old, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		a, aok := old.(map[string]any)
		b, bok := v.(map[string]any)
		if aok && bok {
			if e := merge(a, b, path+"."+k); e != nil {
				return e
			}
			continue
		}
		if !bytes.Equal(encode(old), encode(v)) {
			return fmt.Errorf("conflicting configuration key %s.%s", path, k)
		}
	}
	return nil
}

func Atomic(path string, b []byte, mode fs.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".atlas-write-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
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
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

func Apply(root string, plan Plan) error {
	root, e := filepath.Abs(root)
	if e != nil {
		return e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	dir, e := safe(root, ".atlas")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	lock := filepath.Join(dir, "init.lock")
	if e = os.Mkdir(lock, 0700); e != nil {
		return fmt.Errorf("another init may be active (lock %s): %w", lock, e)
	}
	defer os.Remove(lock)
	journal := filepath.Join(dir, "init-recovery.json")
	if _, e = os.Stat(journal); e == nil {
		return fmt.Errorf("unfinished init: recover using %s before retrying", journal)
	}
	for _, f := range plan.Files {
		full, err := safe(root, f.Path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(full)
		if f.Existed {
			if err != nil || !bytes.Equal(b, f.Old) {
				return fmt.Errorf("%s changed since planning", f.Path)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%s appeared since planning", f.Path)
		}
	}
	type backup struct {
		Path    string
		Data    []byte
		Existed bool
		Mode    fs.FileMode
	}
	backups := []backup{}
	for _, f := range plan.Files {
		if f.Action != "unchanged" {
			backups = append(backups, backup{f.Path, f.Old, f.Existed, f.OldMode})
		}
	}
	if len(backups) == 0 {
		return nil
	}
	if e = Atomic(journal, encode(backups), 0600); e != nil {
		return e
	}
	applied := []File{}
	for _, f := range plan.Files {
		if f.Action == "unchanged" {
			continue
		}
		full, err := safe(root, f.Path)
		if err == nil {
			// Include the current file in rollback even if an error follows
			// rename (for example directory fsync failure).
			applied = append(applied, f)
			err = Atomic(full, f.Data, f.Mode)
		}
		if err != nil {
			rollbackOK := true
			for i := len(applied) - 1; i >= 0; i-- {
				a := applied[i]
				p := filepath.Join(root, a.Path)
				var re error
				if a.Existed {
					re = Atomic(p, a.Old, a.OldMode)
				} else {
					re = os.Remove(p)
					if os.IsNotExist(re) {
						re = nil
					}
				}
				if re != nil {
					rollbackOK = false
				}
			}
			if rollbackOK {
				_ = os.Remove(journal)
			}
			return fmt.Errorf("initialization failed; recovery journal %s: %w", journal, err)
		}
	}
	return os.Remove(journal)
}
