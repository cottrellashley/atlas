package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/native"
	"github.com/cottrellashley/atlas/internal/picker"
)

func (a *App) nativeSessions(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	op := args[0]
	if op == "new" {
		return a.newSession(ctx, args[1:])
	}
	if op != "list" && op != "open" && op != "resume" {
		return errors.New("usage: atlas session list | atlas session open [ID] | atlas session new")
	}
	f := a.flags("session " + op)
	id := f.String("harness", "", "filter by configured harness")
	cwd := f.String("cwd", "", "filter by project directory (default: all projects)")
	query := f.String("search", "", "search titles, IDs, and project directories")
	limit := f.Int("limit", 100, "recent sessions per harness, up to 500")
	j := f.Bool("json", false, "JSON groups with native session handles")
	plain := f.Bool("plain", false, "print the list without opening the picker")
	if err := parse(f, args[1:]); err != nil {
		return err
	}
	if (op == "list" && f.NArg() != 0) || f.NArg() > 1 {
		return errors.New("usage: atlas session list [--search TEXT] | atlas resume [ID] [--harness ID]")
	}
	if op != "list" && (*j || *plain) {
		return errors.New("--json and --plain are for session list; use atlas resume ID to open a session")
	}
	if *limit < 1 || *limit > 500 {
		return errors.New("--limit must be between 1 and 500 per harness")
	}
	if *cwd != "" {
		abs, err := filepath.Abs(*cwd)
		if err != nil {
			return err
		}
		*cwd = abs
	}
	c, err := config.Load(a.Home)
	if err != nil {
		return err
	}
	configs := configuredHarnesses(c)
	if *id != "" {
		cfg, err := c.Resolve(*id)
		if err != nil {
			return err
		}
		configs = []harness.Config{cfg}
	}
	var groups []native.Group
	if f.NArg() == 1 {
		groups = native.Find(ctx, configs, *cwd, f.Arg(0))
	} else {
		groups = native.List(ctx, configs, *cwd, *limit)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := range groups {
		filtered := make([]harness.NativeSession, 0, len(groups[i].Sessions))
		for _, s := range groups[i].Sessions {
			text := strings.ToLower(s.ID + " " + s.Title + " " + s.CWD + " " + groups[i].Harness + " " + harnessLabel(groups[i].Adapter))
			match := true
			for _, word := range strings.Fields(strings.ToLower(*query)) {
				match = match && strings.Contains(text, word)
			}
			if match {
				filtered = append(filtered, s)
			}
		}
		groups[i].Sessions = filtered
	}
	if *j {
		return a.json(groups)
	}
	type selection struct {
		cfg     harness.Config
		session harness.NativeSession
	}
	var selections []selection
	var items []picker.Item
	failed := 0
	for _, group := range groups {
		if group.Error != "" {
			failed++
			fmt.Fprintf(a.Err, "%s: %s\n", display(group.Harness), display(group.Error))
		}
		for _, s := range group.Sessions {
			if f.NArg() == 1 && s.ID != f.Arg(0) {
				continue
			}
			cfg, _ := c.Resolve(group.Harness)
			selections = append(selections, selection{cfg, s})
			label := harnessLabel(group.Adapter)
			if group.Harness != group.Adapter {
				label += " · " + group.Harness
			}
			title := s.Title
			if title == "" {
				title = s.ID
			}
			items = append(items, picker.Item{Title: title, Group: label, Detail: s.CWD + " · " + s.UpdatedAt.Local().Format("Jan 02 15:04"), Search: s.ID + " " + group.Harness})
		}
	}
	if f.NArg() == 1 {
		if len(selections) == 0 {
			return fmt.Errorf("native session %q was not found; check --harness and --cwd", f.Arg(0))
		}
		if len(selections) > 1 {
			return errors.New("that native session ID appears under multiple harnesses; choose one with --harness ID")
		}
		s := selections[0]
		return a.openNative(ctx, s.cfg, s.session.CWD, &s.session, nil)
	}
	if *plain || !a.interactive() {
		fmt.Fprintf(a.Out, "Up to %d recent sessions per harness. Use --limit 500 to browse more.\n\n", *limit)
		for _, group := range groups {
			fmt.Fprintf(a.Out, "%s (%s)\n", harnessLabel(group.Adapter), display(group.Harness))
			if group.Error != "" {
				fmt.Fprintln(a.Out, "  Unavailable; see diagnostic above.")
			} else if len(group.Sessions) == 0 {
				fmt.Fprintln(a.Out, "  No sessions found.")
			}
			for _, s := range group.Sessions {
				fmt.Fprintf(a.Out, "  %s  %s\n    %s · %s\n", display(s.ID), display(s.Title), display(s.CWD), s.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
		}
		if op != "list" {
			return errors.New("choose a native session with atlas resume ID --harness ID, or run atlas resume in a terminal")
		}
		if failed == len(groups) && failed > 0 {
			return errors.New("no harness history could be read")
		}
		return nil
	}
	if len(items) == 0 {
		if failed > 0 {
			return errors.New("no sessions available; some harness histories could not be read (see diagnostics)")
		}
		fmt.Fprintln(a.Out, "No sessions found. Start one with atlas new.")
		return nil
	}
	i, err := a.pick(ctx, fmt.Sprintf("Continue a session · latest %d per harness", *limit), items)
	if err != nil {
		return err
	}
	s := selections[i]
	return a.openNative(ctx, s.cfg, s.session.CWD, &s.session, nil)
}
