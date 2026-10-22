package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/catalog"
	"github.com/cottrellashley/atlas/internal/project"
)

type agentTemplate struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
}

func (a *App) availableAgents(ctx context.Context, catalogName string) ([]agentTemplate, error) {
	out := []agentTemplate{{Name: "researcher", Source: "researcher", Version: project.ResearcherVersion, Description: "Research questions, gather evidence, and write sourced findings", Origin: "built-in"}}
	entries, err := os.ReadDir(filepath.Join(a.Home, "templates"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "researcher" {
			continue
		}
		source, err := filepath.Abs(filepath.Join(a.Home, "templates", entry.Name()))
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(source, "template.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Fprintf(a.Err, "Skipping agent %s: %s\n", display(entry.Name()), display(err.Error()))
			continue
		}
		var m project.Manifest
		if err = json.Unmarshal(data, &m); err != nil {
			fmt.Fprintf(a.Err, "Skipping agent %s: %s\n", display(entry.Name()), display(err.Error()))
			continue
		}
		if m.SchemaVersion != 1 || m.ID == "" {
			fmt.Fprintf(a.Err, "Skipping agent %s: invalid template manifest\n", display(entry.Name()))
			continue
		}
		out = append(out, agentTemplate{Name: entry.Name(), Source: source, Version: m.Version, Description: "Local project template", Origin: "local"})
	}
	// Remote catalogs are opt-in: opening Atlas must not require a network or
	// credentials just to show local agents.
	if catalogName != "" {
		settings, err := catalog.Load(a.Home)
		if err != nil {
			return nil, err
		}
		_, cfg, err := settings.Resolve(catalogName)
		if err != nil {
			return nil, err
		}
		client, err := catalog.New(ctx, a.Home, cfg)
		if err != nil {
			return nil, err
		}
		entries, err := client.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			m := entry.Manifest
			if m.Kind != "project-template" {
				continue
			}
			source := catalogName + "::" + m.ID + "@" + m.Version
			out = append(out, agentTemplate{Name: m.ID, Source: source, Version: m.Version, Description: m.Description, Origin: catalogName})
		}
	}
	return out, nil
}

func (a *App) agents(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] != "list" {
		return errors.New("usage: atlas agent list [--catalog NAME] [--json]")
	}
	f := a.flags("agent list")
	cat := f.String("catalog", "", "include project templates from this catalog")
	j := f.Bool("json", false, "JSON output")
	if err := parse(f, args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("agent list takes no positional arguments")
	}
	agents, err := a.availableAgents(ctx, *cat)
	if err != nil {
		return err
	}
	if *j {
		return a.json(agents)
	}
	for _, agent := range agents {
		fmt.Fprintf(a.Out, "%s  [%s]\n  %s\n", display(agent.Source), display(agent.Origin), display(agent.Description))
	}
	fmt.Fprintln(a.Out, "\nStart with: atlas init researcher")
	if strings.TrimSpace(*cat) == "" {
		fmt.Fprintln(a.Out, "Shared agents: atlas agent list --catalog NAME")
	}
	return nil
}
