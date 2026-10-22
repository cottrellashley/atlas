package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/native"
	"github.com/cottrellashley/atlas/internal/picker"
	"github.com/cottrellashley/atlas/internal/process"
	"github.com/cottrellashley/atlas/internal/project"
)

func (a *App) interactive() bool {
	return a.choose != nil || picker.IsTerminal(a.In, a.Out)
}

func (a *App) pick(ctx context.Context, title string, items []picker.Item) (int, error) {
	if a.choose != nil {
		return a.choose(ctx, title, items)
	}
	return picker.Choose(ctx, a.In, a.Out, title, items)
}

func (a *App) home(ctx context.Context) error {
	if !a.interactive() {
		return a.help()
	}
	choice, err := a.pick(ctx, "Atlas", []picker.Item{
		{Title: "New session", Detail: "Start here, with an optional agent template"},
		{Title: "Continue session", Detail: "Find a conversation in Claude Code, OpenCode, Pi, or Oh-my-pi"},
	})
	if err != nil {
		return err
	}
	if choice == 1 {
		return a.nativeSessions(ctx, nil)
	}
	p, _, err := project.Load(".")
	if err != nil {
		return err
	}
	if p != nil {
		return a.newSession(ctx, nil)
	}
	agents, err := a.availableAgents(ctx, "")
	if err != nil {
		return err
	}
	items := []picker.Item{{Title: "Blank project", Detail: "Open a harness with this directory's existing configuration"}}
	for _, agent := range agents {
		items = append(items, picker.Item{Title: agent.Name, Detail: agent.Description, Group: "Agents"})
	}
	choice, err = a.pick(ctx, "Start with…", items)
	if err != nil {
		return err
	}
	if choice == 0 {
		return a.newSession(ctx, nil)
	}
	return a.init(ctx, []string{agents[choice-1].Source})
}

func harnessLabel(adapter string) string {
	switch adapter {
	case "claude":
		return "Claude Code"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	case "omp":
		return "Oh-my-pi"
	default:
		return adapter
	}
}

func configuredHarnesses(c config.Config) []harness.Config {
	out := make([]harness.Config, 0, len(c.Harnesses))
	for _, cfg := range c.Harnesses {
		out = append(out, cfg)
	}
	rank := map[string]int{"claude": 0, "opencode": 1, "pi": 2, "omp": 3}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Adapter != out[j].Adapter {
			return rank[out[i].Adapter] < rank[out[j].Adapter]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func executable(cfg harness.Config, cwd string) error {
	path, err := process.ResolveExecutable(cfg, cwd)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}

// Project selection remains explicit for automation. Interactive users only see
// installed harnesses, and existing projects keep their configured alias.
func (a *App) selectHarness(ctx context.Context, id, cwd string, requireInstalled, allowPrompt bool) (harness.Config, string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return harness.Config{}, "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return harness.Config{}, abs, err
	}
	if !info.IsDir() {
		return harness.Config{}, abs, fmt.Errorf("%s is not a directory", abs)
	}
	c, err := config.Load(a.Home)
	if err != nil {
		return harness.Config{}, abs, err
	}
	if id == "" {
		p, _, err := project.Load(abs)
		if err != nil {
			return harness.Config{}, abs, err
		}
		if p != nil {
			id = p.Harness
		}
	}
	if id != "" {
		cfg, err := c.Resolve(id)
		if err == nil && requireInstalled {
			err = executable(cfg, abs)
		}
		return cfg, abs, err
	}
	var installed []harness.Config
	var items []picker.Item
	for _, cfg := range configuredHarnesses(c) {
		if executable(cfg, abs) != nil {
			continue
		}
		installed = append(installed, cfg)
		items = append(items, picker.Item{Title: harnessLabel(cfg.Adapter), Detail: cfg.ID})
	}
	if len(installed) == 0 {
		return harness.Config{}, abs, errors.New("install Claude Code, OpenCode, Pi, or Oh-my-pi first; use --harness ID --no-open to prepare a project without launching")
	}
	if len(installed) == 1 {
		return installed[0], abs, nil
	}
	if !allowPrompt || !a.interactive() {
		return harness.Config{}, abs, errors.New("choose a harness with --harness claude, opencode, pi, or omp (configured aliases also work)")
	}
	i, err := a.pick(ctx, "Open with…", items)
	if err != nil {
		return harness.Config{}, abs, err
	}
	return installed[i], abs, nil
}

func (a *App) newSession(ctx context.Context, args []string) error {
	f := a.flags("new")
	id := f.String("harness", "", "configured harness")
	cwd := f.String("cwd", ".", "project directory")
	if err := parse(f, args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return errors.New("usage: atlas new [AGENT] [--harness ID] [--cwd DIR]")
	}
	if f.NArg() == 1 {
		return a.init(ctx, []string{f.Arg(0), "--harness", *id, "--cwd", *cwd})
	}
	cfg, abs, err := a.selectHarness(ctx, *id, *cwd, true, true)
	if err != nil {
		return err
	}
	p, _, err := project.Load(abs)
	if err != nil {
		return err
	}
	return a.openNative(ctx, cfg, abs, nil, p)
}

func launchArgs(cfg harness.Config, cwd string, session *harness.NativeSession, p *project.Project) ([]string, error) {
	var options native.StartOptions
	if p != nil && session == nil {
		role := p.Agents[p.DefaultAgent]
		options = native.StartOptions{Model: role.Model, Thinking: role.Thinking, NativeAgent: role.NativeAgent, PermissionMode: role.PermissionMode}
	}
	return native.ArgsWithOptionsForCWD(cfg, cwd, session, options)
}

func (a *App) checkLaunch(cfg harness.Config, cwd string, session *harness.NativeSession, p *project.Project) error {
	if _, err := launchArgs(cfg, cwd, session, p); err != nil {
		return err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return fmt.Errorf("cannot open project directory %s: %w", cwd, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project directory %s is not a directory", cwd)
	}
	return executable(cfg, cwd)
}

func (a *App) openNative(ctx context.Context, cfg harness.Config, cwd string, session *harness.NativeSession, p *project.Project) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session != nil {
		cwd = session.CWD
		if !filepath.IsAbs(cwd) {
			return errors.New("native session is missing an absolute project directory")
		}
	}
	if err := a.checkLaunch(cfg, cwd, session, p); err != nil {
		return err
	}
	args, err := launchArgs(cfg, cwd, session, p)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "Opening %s in %s…\n", harnessLabel(cfg.Adapter), display(cwd))
	if a.launch != nil {
		return a.launch(cfg, cwd, args)
	}
	return handoff(cfg, cwd, args)
}

// Native session titles and paths can contain arbitrary text. Never emit their
// terminal controls, even in the plain listing; JSON preserves the real values.
func display(s string) string {
	return picker.Sanitize(s)
}

func (a *App) agentHelp() error {
	_, err := fmt.Fprint(a.Out, `Atlas tools for agents

  atlas agent list [--catalog NAME] [--json]
  atlas init AGENT --harness ID --no-open [--package SOURCE]
  atlas init AGENT --harness ID --dry-run --json
  atlas session list [--harness ID] [--cwd DIR] [--search TEXT] --json
  atlas skill add DIRECTORY|PACKAGE [--harness ID] [--cwd DIR]
  atlas mcp add NAME --command EXE [--arg ARG] [--harness ID]
  atlas mcp add NAME --url URL [--header 'Name=Value'] [--harness ID]
  atlas package search|add|status|sync|update|remove|recover|validate|publish
  atlas catalog add|create NAME OWNER/REPO [--host HOST]
  atlas catalog list|remove
  atlas github login --token-stdin | status [--host HOST]
  atlas harnesses / atlas doctor [--harness ID] [--deep]
  atlas models [--harness ID] [--cwd DIR] [--json]
  atlas config path|show
  atlas exec [--harness ID] [--cwd DIR] -- [native arguments...]

Package, skill, and MCP changes support --dry-run and --json. Prefer project
scope; use --scope user only when requested. Existing user edits are protected.
init --json and --dry-run never open a harness; --no-open is setup only.
Native harnesses own session history, authentication, permissions, and trust.
Configuration: $ATLAS_HOME/config.json (default ~/.atlas/config.json).
`)
	return err
}
