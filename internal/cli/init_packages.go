package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/install"
	"github.com/cottrellashley/atlas/internal/project"
	"github.com/cottrellashley/atlas/internal/resolve"
)

func (a *App) initWithPackages(ctx context.Context, cwd, cat string, cfg harness.Config, template project.Plan, sources []string, dry, jsonOutput bool) error {
	root, e := filepath.Abs(cwd)
	if e != nil {
		return e
	}
	resolver, e := a.resolver(cat)
	if e != nil {
		return e
	}
	declaration, old, e := resolve.Read(root)
	if e != nil {
		return e
	}
	if e = checkPackageScope(root, "project", declaration, old); e != nil {
		return e
	}
	requests := append([]resolve.Request{}, declaration.Packages...)
	updates := map[string]bool{}
	for _, source := range sources {
		pkg, e := resolver.Fetch(ctx, source)
		if e != nil {
			return e
		}
		defer pkg.Close()
		if strings.HasPrefix(pkg.Source, "file:") {
			source = pkg.Source
		}
		request := resolve.Request{Source: source, Harness: cfg.Adapter, HarnessID: cfg.ID, ID: pkg.Manifest.ID}
		found := false
		for i, old := range requests {
			if old.Harness == cfg.Adapter && old.ID == pkg.Manifest.ID {
				requests[i] = request
				found = true
				break
			}
		}
		if !found {
			requests = append(requests, request)
		}
		updates[source] = true
	}
	base, metadata := []install.File{}, []install.File{}
	for _, f := range template.Files {
		file := install.File{Path: f.Path, Data: f.Data, Mode: f.Mode}
		if f.Path == ".atlas/project.json" {
			metadata = append(metadata, file)
		} else {
			base = append(base, file)
		}
	}
	transaction, lock, e := a.packagePlan(ctx, root, "project", cfg.ID, resolver, requests, old, updates, base, metadata)
	if e != nil {
		return e
	}
	// Ensure the earlier template build was based on the same files included in
	// the combined transaction. Apply independently checks its snapshots again.
	for _, f := range template.Files {
		b, err := os.ReadFile(filepath.Join(root, f.Path))
		if f.Existed {
			if err != nil || !bytes.Equal(b, f.Old) {
				return fmt.Errorf("%s changed during initialization planning", f.Path)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%s appeared during initialization planning", f.Path)
		}
	}
	if !dry {
		if e = install.Apply(root, transaction); e != nil {
			return e
		}
	}
	if jsonOutput {
		return a.json(map[string]any{"template": template.Project, "plan": transaction, "lock": lock, "dryRun": dry})
	}
	for _, f := range transaction.Files {
		fmt.Fprintf(a.Out, "%-10s %s\n", f.Action, f.Path)
	}
	if dry {
		fmt.Fprintln(a.Out, "Dry run; no project files written.")
	} else {
		fmt.Fprintf(a.Out, "Initialized %s with %s and %d package releases.\n", template.Project.Template, cfg.ID, len(lock.Packages))
		fmt.Fprintln(a.Out, "Launch with atlas tui or the native provider command. Native project trust still applies.")
	}
	return nil
}
