package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/cottrellashley/atlas/internal/adapters"
	"github.com/cottrellashley/atlas/internal/config"
	"github.com/cottrellashley/atlas/internal/daemon"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
	"github.com/cottrellashley/atlas/internal/project"
	"github.com/cottrellashley/atlas/internal/store"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Version = "0.1.0"

type App struct {
	Out, Err io.Writer
	In       io.Reader
	Home     string
}

func New() (*App, error) {
	h, e := config.Home()
	return &App{Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Home: h}, e
}
func (a *App) json(v any) error {
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func (a *App) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(a.Err)
	return f
}

// Interspersed flags permit both `run --model X prompt` and `run prompt --model X`.
func parse(f *flag.FlagSet, args []string) error {
	options := []string{}
	positional := []string{}
	for i := 0; i < len(args); i++ {
		v := args[i]
		if v == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(v, "-") && v != "-" {
			options = append(options, v)
			key := strings.TrimLeft(v, "-")
			if strings.Contains(key, "=") {
				continue
			}
			def := f.Lookup(key)
			if def == nil {
				continue
			}
			isBool := false
			if b, ok := def.Value.(interface{ IsBoolFlag() bool }); ok {
				isBool = b.IsBoolFlag()
			}
			if !isBool && i+1 < len(args) {
				i++
				options = append(options, args[i])
			}
		} else {
			positional = append(positional, v)
		}
	}
	return f.Parse(append(append(options, "--"), positional...))
}
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.help()
	}
	switch args[0] {
	case "help", "--help", "-h":
		return a.help()
	case "version", "--version":
		fmt.Fprintln(a.Out, "atlas "+Version)
		return nil
	case "init":
		return a.init(args[1:])
	case "templates":
		return a.templates(args[1:])
	case "harnesses", "doctor":
		return a.doctor(ctx, args[1:], args[0] == "doctor")
	case "models":
		return a.models(ctx, args[1:])
	case "run":
		return a.run(ctx, args[1:])
	case "send", "resume":
		return a.send(ctx, args[1:])
	case "sessions":
		return a.sessions(ctx, args[1:])
	case "logs":
		return a.logs(ctx, args[1:])
	case "wait":
		return a.wait(ctx, args[1:])
	case "stop":
		return a.stop(ctx, args[1:])
	case "permissions":
		return a.permissions(ctx, args[1:])
	case "configure":
		return a.configure(ctx, args[1:])
	case "exec":
		return a.exec(ctx, args[1:])
	case "daemon":
		return a.daemon(ctx, args[1:])
	case "config":
		return a.config(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run atlas help", args[0])
	}
}
func (a *App) help() error {
	_, e := fmt.Fprint(a.Out, `Atlas — project templates and coding harness sessions

  atlas init --harness opencode|pi|claude --template researcher [--cwd DIR]
  atlas init --harness pi --template ./my-template --dry-run
  atlas templates list
  atlas harnesses                         List configured harnesses
  atlas doctor [--harness ID] [--deep]      Check binaries; optionally catalogs
  atlas models [--harness ID] [--cwd DIR] [--json]
  atlas run [--harness ID] [--agent ROLE] [--model ID] "prompt"
  atlas run --background "prompt"          Keep running after this command exits
  atlas sessions list [--all] [--json]
  atlas sessions show ID
  atlas sessions discover --harness ID [--cwd DIR]
  atlas sessions import --harness ID --handle NATIVE_ID [--cwd DIR]
  atlas sessions archive|unarchive ID
  atlas send ID "follow-up" [--background]
  atlas resume ID "follow-up"              Alias for send
  atlas logs ID [--follow] [--json] [--after SEQ]
  atlas wait ID [--timeout 10m] [--json]    A wait timeout does not stop work
  atlas stop ID                           Wait for cancellation and cleanup
  atlas permissions list ID
  atlas permissions allow|deny ID PERMISSION_ID [--text ANSWER]
  atlas configure ID [--model ID] [--thinking LEVEL] [--native-agent NAME]
  atlas exec [--harness ID] [--cwd DIR] -- [native arguments...]
  atlas daemon start|status|stop
  atlas config path|show

Common run flags: --cwd, --model, --thinking, --native-agent, --permission-mode,
--trust-project (Pi only), --background, --json, --timeout. Omit --model to use
the harness's native default. Model IDs remain native, including their slashes.

Configuration: $ATLAS_HOME/config.json (default ~/.atlas/config.json).
Native harness installations and authentication remain under your control.
Atlas runs inside Linux/WSL. Use atlas exec for native interactive login.
`)
	return e
}
func (a *App) init(args []string) error {
	f := a.flags("init")
	h := f.String("harness", "", "configured harness ID")
	t := f.String("template", "researcher", "template name or directory")
	cwd := f.String("cwd", ".", "project directory")
	dry := f.Bool("dry-run", false, "show plan without writing")
	j := f.Bool("json", false, "JSON plan")
	if e := parse(f, args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("init does not accept positional arguments")
	}
	if *h == "" {
		return errors.New("--harness is required")
	}
	c, e := config.Load(a.Home)
	if e != nil {
		return e
	}
	cfg, e := c.Resolve(*h)
	if e != nil {
		return e
	}
	plan, e := project.Build(*cwd, a.Home, *h, cfg.Adapter, *t)
	if e != nil {
		return e
	}
	if !*dry {
		if e = project.Apply(*cwd, plan); e != nil {
			return e
		}
	}
	if *j {
		return a.json(plan)
	}
	for _, file := range plan.Files {
		fmt.Fprintf(a.Out, "%-10s %s\n", file.Action, file.Path)
	}
	if *dry {
		fmt.Fprintln(a.Out, "Dry run; no files written.")
	} else {
		fmt.Fprintf(a.Out, "Initialized %s with %s (%s).\n", plan.Project.Template, *h, cfg.Adapter)
		if cfg.Adapter == "pi" {
			fmt.Fprintln(a.Out, "Pi project resources require native trust. Review them, then use --trust-project when running with Atlas, or approve through pi.")
		}
	}
	return nil
}
func (a *App) templates(args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "list") {
		return errors.New("usage: atlas templates list")
	}
	fmt.Fprintln(a.Out, "researcher\tbuilt-in\t1.0.0")
	entries, e := os.ReadDir(filepath.Join(a.Home, "templates"))
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, d := range entries {
		if d.IsDir() {
			if _, e := os.Stat(filepath.Join(a.Home, "templates", d.Name(), "template.json")); e == nil {
				fmt.Fprintf(a.Out, "%s\tlocal\n", d.Name())
			}
		}
	}
	return nil
}
func (a *App) selection(id, cwd string) (harness.Config, *project.Project, string, error) {
	abs, e := filepath.Abs(cwd)
	if e != nil {
		return harness.Config{}, nil, "", e
	}
	p, _, e := project.Load(abs)
	if e != nil {
		return harness.Config{}, nil, "", e
	}
	if id == "" && p != nil {
		id = p.Harness
	}
	if id == "" {
		return harness.Config{}, p, abs, errors.New("choose --harness or initialize this project with atlas init")
	}
	c, e := config.Load(a.Home)
	if e != nil {
		return harness.Config{}, p, abs, e
	}
	h, e := c.Resolve(id)
	return h, p, abs, e
}
func (a *App) doctor(ctx context.Context, args []string, probe bool) error {
	f := a.flags("doctor")
	id := f.String("harness", "", "one harness")
	cwd := f.String("cwd", ".", "project directory")
	deep := f.Bool("deep", false, "probe model catalogs")
	j := f.Bool("json", false, "JSON output")
	if e := parse(f, args); e != nil {
		return e
	}
	c, e := config.Load(a.Home)
	if e != nil {
		return e
	}
	ids := []string{}
	if *id != "" {
		ids = append(ids, *id)
	} else {
		for id := range c.Harnesses {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	rows := []map[string]any{}
	failed := false
	for _, id := range ids {
		cfg, e := c.Resolve(id)
		if e != nil {
			return e
		}
		row := map[string]any{"id": id, "adapter": cfg.Adapter, "command": cfg.Command}
		if probe {
			pc, cancel := context.WithTimeout(ctx, 15*time.Second)
			out, err := process.Command(pc, cfg, *cwd, "--version").CombinedOutput()
			cancel()
			if err != nil {
				row["error"] = err.Error()
				failed = true
			} else {
				row["version"] = strings.TrimSpace(string(out))
			}
			if *deep && err == nil {
				dc, stop := context.WithTimeout(ctx, 60*time.Second)
				driver, err := adapters.New(cfg)
				var catalog harness.Catalog
				if err == nil {
					catalog, err = driver.Catalog(dc, *cwd)
				}
				stop()
				if err != nil {
					row["error"] = err.Error()
					failed = true
				} else {
					row["models"] = len(catalog.Models)
				}
			}
		}
		rows = append(rows, row)
	}
	if *j {
		if e = a.json(rows); e != nil {
			return e
		}
	} else {
		for _, row := range rows {
			fmt.Fprintf(a.Out, "%s\t%s\t%v", row["id"], row["adapter"], row["command"])
			if v := row["version"]; v != nil {
				fmt.Fprintf(a.Out, "\t%v", v)
			}
			if v := row["models"]; v != nil {
				fmt.Fprintf(a.Out, "\t%v models", v)
			}
			if v := row["error"]; v != nil {
				fmt.Fprintf(a.Out, "\tERROR: %v", v)
			}
			fmt.Fprintln(a.Out)
		}
	}
	if failed {
		return errors.New("one or more harness checks failed")
	}
	return nil
}
func (a *App) models(ctx context.Context, args []string) error {
	f := a.flags("models")
	id := f.String("harness", "", "harness")
	cwd := f.String("cwd", ".", "directory")
	j := f.Bool("json", false, "JSON")
	timeout := f.Duration("timeout", 60*time.Second, "catalog deadline")
	if e := parse(f, args); e != nil {
		return e
	}
	cfg, _, abs, e := a.selection(*id, *cwd)
	if e != nil {
		return e
	}
	driver, e := adapters.New(cfg)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	cat, e := driver.Catalog(ctx, abs)
	if e != nil {
		return e
	}
	if *j {
		return a.json(cat)
	}
	for _, m := range cat.Models {
		mark := ""
		if m.Default {
			mark = " (native default)"
		}
		fmt.Fprintf(a.Out, "%s\t%s%s\n", m.ID, m.Name, mark)
	}
	if len(cat.Models) == 0 {
		fmt.Fprintln(a.Out, "No models available. Configure native harness authentication.")
	}
	return nil
}
func (a *App) client(ctx context.Context, start bool) (*daemon.Client, error) {
	c := daemon.NewClient(a.Home)
	pc, cancel := context.WithTimeout(ctx, time.Second)
	e := c.Ping(pc)
	cancel()
	if e == nil {
		return c, nil
	}
	if !start {
		return nil, fmt.Errorf("Atlas daemon is not reachable: %w", e)
	}
	if e = os.MkdirAll(a.Home, 0700); e != nil {
		return nil, e
	}
	log, e := os.OpenFile(filepath.Join(a.Home, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer log.Close()
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	cmd := exec.Command(exe, "__serve")
	cmd.Env = append(process.Environment(nil), "ATLAS_HOME="+a.Home)
	cmd.Stdout = log
	cmd.Stderr = log
	detach(cmd)
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	go cmd.Wait()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("daemon did not start; inspect %s", filepath.Join(a.Home, "daemon.log"))
		case <-tick.C:
			pc, cancel := context.WithTimeout(ctx, time.Second)
			e = c.Ping(pc)
			cancel()
			if e == nil {
				return c, nil
			}
		}
	}
}
func (a *App) prompt(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("prompt is required (use - to read stdin)")
	}
	s := strings.Join(args, " ")
	if s == "-" {
		b, e := io.ReadAll(io.LimitReader(a.In, (16<<20)+1))
		if len(b) > 16<<20 {
			return "", errors.New("prompt exceeds 16 MiB")
		}
		return string(b), e
	}
	return s, nil
}
func (a *App) run(ctx context.Context, args []string) error {
	f := a.flags("run")
	id := f.String("harness", "", "harness")
	cwd := f.String("cwd", ".", "directory")
	role := f.String("agent", "", "Atlas project agent")
	model := f.String("model", "", "native model ID")
	thinking := f.String("thinking", "", "thinking level")
	native := f.String("native-agent", "", "native agent")
	mode := f.String("permission-mode", "", "native permission mode")
	trust := f.Bool("trust-project", false, "approve Pi project resources")
	bg := f.Bool("background", false, "run in background")
	j := f.Bool("json", false, "JSON events")
	timeout := f.Duration("timeout", 0, "wait timeout (does not cancel work)")
	if e := parse(f, args); e != nil {
		return e
	}
	prompt, e := a.prompt(f.Args())
	if e != nil {
		return e
	}
	cfg, p, abs, e := a.selection(*id, *cwd)
	if e != nil {
		return e
	}
	if p != nil {
		if *role == "" {
			*role = p.DefaultAgent
		}
		agent, ok := p.Agents[*role]
		if !ok {
			return fmt.Errorf("unknown project agent %q", *role)
		}
		visited := map[string]bool{}
		f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
		if !visited["model"] {
			*model = agent.Model
		}
		if !visited["thinking"] {
			*thinking = agent.Thinking
		}
		if !visited["native-agent"] {
			*native = agent.NativeAgent
		}
		if !visited["permission-mode"] {
			*mode = agent.PermissionMode
		}
	} else if *role != "" {
		return errors.New("--agent requires an initialized Atlas project")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	var r store.Record
	in := daemon.RunInput{Harness: cfg.ID, CWD: abs, Agent: *role, Prompt: prompt, Model: *model, Thinking: *thinking, NativeAgent: *native, PermissionMode: *mode, TrustProject: *trust}
	if e = c.Call(ctx, "POST", "/run", in, &r); e != nil {
		return e
	}
	if *bg {
		if *j {
			return a.json(r)
		}
		fmt.Fprintln(a.Out, r.ID)
		return nil
	}
	fmt.Fprintf(a.Err, "Session %s\n", r.ID)
	return a.follow(ctx, c, r.ID, 0, *j, *timeout, true)
}
func (a *App) send(ctx context.Context, args []string) error {
	f := a.flags("send")
	bg := f.Bool("background", false, "background")
	j := f.Bool("json", false, "JSON")
	timeout := f.Duration("timeout", 0, "wait timeout")
	if e := parse(f, args); e != nil {
		return e
	}
	if f.NArg() < 2 {
		return errors.New("usage: atlas send ID prompt")
	}
	id := f.Arg(0)
	prompt, e := a.prompt(f.Args()[1:])
	if e != nil {
		return e
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	var previous, r store.Record
	if e = c.Call(ctx, "GET", "/session?id="+url.QueryEscape(id), nil, &previous); e != nil {
		return e
	}
	if e = c.Call(ctx, "POST", "/send", map[string]string{"id": id, "prompt": prompt}, &r); e != nil {
		return e
	}
	if *bg {
		if *j {
			return a.json(r)
		}
		fmt.Fprintln(a.Out, r.ID)
		return nil
	}
	fmt.Fprintf(a.Err, "Session %s\n", id)
	return a.follow(ctx, c, id, previous.Seq, *j, *timeout, true)
}
func active(r store.Record) bool { return r.State == "running" || r.State == "waiting_permission" }
func (a *App) showEvent(e harness.Event, j bool) error {
	if j {
		return json.NewEncoder(a.Out).Encode(e)
	}
	switch e.Type {
	case "text":
		_, err := fmt.Fprint(a.Out, e.Text)
		return err
	case "permission":
		if e.Permission != nil {
			fmt.Fprintf(a.Err, "\nPermission %s: %s %s\nReply: atlas permissions allow|deny %s %s\n", e.Permission.ID, e.Permission.Tool, e.Permission.Description, e.SessionID, e.Permission.ID)
		}
	case "notice":
		fmt.Fprintln(a.Err, e.Text)
	}
	return nil
}
func (a *App) follow(ctx context.Context, c *daemon.Client, id string, after int64, j bool, timeout time.Duration, stopOnSignal bool) (retErr error) {
	defer func() {
		if ctx.Err() != nil && stopOnSignal {
			stopCtx, stop := context.WithTimeout(context.Background(), 35*time.Second)
			defer stop()
			if err := c.Call(stopCtx, "POST", "/stop", map[string]string{"id": id}, nil); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("cancellation not confirmed: %w", err))
			}
		}
	}()
	waitCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var events []harness.Event
		if e := c.Call(waitCtx, "GET", fmt.Sprintf("/events?id=%s&after=%d", url.QueryEscape(id), after), nil, &events); e != nil {
			return e
		}
		for _, event := range events {
			if e := a.showEvent(event, j); e != nil {
				return e
			}
			after = event.Seq
		}
		var r store.Record
		if e := c.Call(waitCtx, "GET", "/session?id="+url.QueryEscape(id), nil, &r); e != nil {
			return e
		}
		if !active(r) && after >= r.Seq {
			if !j {
				fmt.Fprintln(a.Out)
			}
			if r.State == "failed" || r.State == "interrupted" {
				return errors.New(r.Error)
			}
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("stopped waiting for %s: %w (use atlas logs/wait to reconnect)", id, waitCtx.Err())
		case <-tick.C:
		}
	}
}
func (a *App) sessions(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		f := a.flags("sessions list")
		j := f.Bool("json", false, "JSON")
		all := f.Bool("all", false, "include archived")
		if e := parse(f, args[1:]); e != nil {
			return e
		}
		c, e := a.client(ctx, true)
		if e != nil {
			return e
		}
		var records []store.Record
		if e = c.Call(ctx, "GET", "/sessions", nil, &records); e != nil {
			return e
		}
		out := []store.Record{}
		for _, r := range records {
			if *all || !r.Archived {
				out = append(out, r)
			}
		}
		if *j {
			return a.json(out)
		}
		for _, r := range out {
			fmt.Fprintf(a.Out, "%s\t%-18s %-8s %s\n", r.ID, r.Harness, r.State, r.Title)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: atlas sessions show ID")
		}
		c, e := a.client(ctx, true)
		if e != nil {
			return e
		}
		var r store.Record
		if e = c.Call(ctx, "GET", "/session?id="+url.QueryEscape(args[1]), nil, &r); e != nil {
			return e
		}
		return a.json(r)
	case "archive", "unarchive":
		if len(args) != 2 {
			return errors.New("usage: atlas sessions archive|unarchive ID")
		}
		c, e := a.client(ctx, true)
		if e != nil {
			return e
		}
		return c.Call(ctx, "POST", "/archive", map[string]any{"id": args[1], "archived": args[0] == "archive"}, nil)
	case "discover", "import":
		f := a.flags("sessions " + args[0])
		id := f.String("harness", "", "harness")
		cwd := f.String("cwd", ".", "directory")
		handle := f.String("handle", "", "native session ID/path")
		limit := f.Int("limit", 50, "maximum results")
		j := f.Bool("json", false, "JSON")
		if e := parse(f, args[1:]); e != nil {
			return e
		}
		cfg, _, abs, e := a.selection(*id, *cwd)
		if e != nil {
			return e
		}
		if args[0] == "import" {
			if *handle == "" {
				return errors.New("--handle is required")
			}
			c, e := a.client(ctx, true)
			if e != nil {
				return e
			}
			var r store.Record
			if e = c.Call(ctx, "POST", "/import", map[string]string{"harness": cfg.ID, "cwd": abs, "handle": *handle}, &r); e != nil {
				return e
			}
			if *j {
				return a.json(r)
			}
			fmt.Fprintln(a.Out, r.ID)
			return nil
		}
		if *limit < 1 || *limit > 5000 {
			return errors.New("--limit must be between 1 and 5000")
		}
		driver, e := adapters.New(cfg)
		if e != nil {
			return e
		}
		dc, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		items, e := driver.Discover(dc, abs, *limit)
		if e != nil {
			return e
		}
		if *j {
			return a.json(items)
		}
		for _, s := range items {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\n", s.ID, s.Title, s.CWD)
		}
		return nil
	default:
		return fmt.Errorf("unknown sessions command %q", args[0])
	}
}
func (a *App) logs(ctx context.Context, args []string) error {
	f := a.flags("logs")
	follow := f.Bool("follow", false, "follow running session")
	j := f.Bool("json", false, "JSON events")
	after := f.Int64("after", 0, "sequence cursor")
	if e := parse(f, args); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("usage: atlas logs ID [--follow]")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	if *follow {
		return a.follow(ctx, c, f.Arg(0), *after, *j, 0, false)
	}
	for {
		var events []harness.Event
		if e = c.Call(ctx, "GET", fmt.Sprintf("/events?id=%s&after=%d", url.QueryEscape(f.Arg(0)), *after), nil, &events); e != nil {
			return e
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if e = a.showEvent(event, *j); e != nil {
				return e
			}
			*after = event.Seq
		}
	}
	if !*j {
		fmt.Fprintln(a.Out)
	}
	return nil
}
func (a *App) wait(ctx context.Context, args []string) error {
	f := a.flags("wait")
	timeout := f.Duration("timeout", 0, "wait timeout")
	j := f.Bool("json", false, "JSON record")
	if e := parse(f, args); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("usage: atlas wait ID")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		var r store.Record
		if e = c.Call(ctx, "GET", "/session?id="+url.QueryEscape(f.Arg(0)), nil, &r); e != nil {
			return e
		}
		if !active(r) {
			if *j {
				if e = a.json(r); e != nil {
					return e
				}
			} else {
				fmt.Fprintln(a.Out, r.State)
			}
			if r.State == "failed" || r.State == "interrupted" {
				return errors.New(r.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait timed out or canceled; session continues: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
func (a *App) stop(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: atlas stop ID")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	return c.Call(ctx, "POST", "/stop", map[string]string{"id": args[0]}, nil)
}
func (a *App) permissions(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: atlas permissions list ID | allow|deny ID PERMISSION_ID [--text ANSWER]")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	if args[0] == "list" {
		var r store.Record
		if e = c.Call(ctx, "GET", "/session?id="+url.QueryEscape(args[1]), nil, &r); e != nil {
			return e
		}
		return a.json(r.Permissions)
	}
	if args[0] != "allow" && args[0] != "deny" {
		return errors.New("permission action must be list, allow or deny")
	}
	f := a.flags("permissions")
	text := f.String("text", "", "text answer for extension dialogs")
	if e = parse(f, args[1:]); e != nil {
		return e
	}
	if f.NArg() != 2 {
		return errors.New("specify session ID and permission ID")
	}
	return c.Call(ctx, "POST", "/permission", map[string]any{"id": f.Arg(0), "control": harness.Control{ID: f.Arg(1), Allow: args[0] == "allow", Text: *text}}, nil)
}
func (a *App) configure(ctx context.Context, args []string) error {
	f := a.flags("configure")
	model := f.String("model", "", "model for subsequent turns; empty clears override")
	thinking := f.String("thinking", "", "thinking")
	native := f.String("native-agent", "", "native agent")
	mode := f.String("permission-mode", "", "permission mode")
	if e := parse(f, args); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("usage: atlas configure ID [--model ID]")
	}
	patch := map[string]*string{}
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "model":
			patch["model"] = model
		case "thinking":
			patch["thinking"] = thinking
		case "native-agent":
			patch["nativeAgent"] = native
		case "permission-mode":
			patch["permissionMode"] = mode
		}
	})
	if len(patch) == 0 {
		return errors.New("provide a setting to change")
	}
	c, e := a.client(ctx, true)
	if e != nil {
		return e
	}
	var r store.Record
	if e = c.Call(ctx, "POST", "/configure", map[string]any{"id": f.Arg(0), "patch": patch}, &r); e != nil {
		return e
	}
	return a.json(r)
}
func (a *App) exec(ctx context.Context, args []string) error {
	f := a.flags("exec")
	id := f.String("harness", "", "harness")
	cwd := f.String("cwd", ".", "directory")
	if e := f.Parse(args); e != nil {
		return e
	}
	cfg, _, abs, e := a.selection(*id, *cwd)
	if e != nil {
		return e
	}
	return handoff(cfg, abs, f.Args())
}
func (a *App) daemon(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: atlas daemon start|status|stop")
	}
	switch args[0] {
	case "start":
		_, e := a.client(ctx, true)
		if e == nil {
			fmt.Fprintln(a.Out, "Atlas daemon running")
		}
		return e
	case "status":
		c, e := a.client(ctx, false)
		if e != nil {
			return e
		}
		var v any
		if e = c.Call(ctx, "GET", "/ping", nil, &v); e != nil {
			return e
		}
		return a.json(v)
	case "stop":
		c, e := a.client(ctx, false)
		if e != nil {
			return e
		}
		if e = c.Call(ctx, "POST", "/shutdown", map[string]any{}, nil); e != nil {
			return e
		}
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if _, e = os.Stat(filepath.Join(a.Home, "daemon.sock")); os.IsNotExist(e) {
				fmt.Fprintln(a.Out, "Atlas daemon stopped")
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		return errors.New("daemon shutdown has not settled")
	default:
		return errors.New("unknown daemon action")
	}
}
func (a *App) config(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: atlas config path|show")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(a.Out, filepath.Join(a.Home, "config.json"))
		return nil
	case "show":
		c, e := config.Load(a.Home)
		if e != nil {
			return e
		}
		for id, h := range c.Harnesses {
			for k := range h.Env {
				h.Env[k] = "<redacted>"
			}
			h.Options = nil
			c.Harnesses[id] = h
		}
		return a.json(c)
	default:
		return errors.New("unknown config command")
	}
}
