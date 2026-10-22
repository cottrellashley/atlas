package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cottrellashley/atlas/internal/harness"
)

func TestEnterpriseHistoryUsesConfiguredNativeAccount(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	root := t.TempDir()
	inherited, configured, home := filepath.Join(root, "inherited"), filepath.Join(root, "configured"), filepath.Join(root, "home")
	for _, store := range []string{inherited, configured, filepath.Join(home, ".claude")} {
		path := filepath.Join(store, "projects", "work", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data := harness.JSON(map[string]any{"type": "user", "cwd": root, "message": map[string]string{"content": store}})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"inherited login context", nil, inherited},
		{"explicit alias login context", map[string]string{"CLAUDE_CONFIG_DIR": configured}, configured},
		{"explicit empty restores configured home", map[string]string{"CLAUDE_CONFIG_DIR": "", "HOME": home}, filepath.Join(home, ".claude")},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &driver{cfg: harness.Config{Env: test.env}}
			for _, lookup := range []func() ([]harness.NativeSession, error){
				func() ([]harness.NativeSession, error) { return d.Discover(context.Background(), "", 10) },
				func() ([]harness.NativeSession, error) { return d.Find(context.Background(), "", id) },
			} {
				sessions, err := lookup()
				if err != nil || len(sessions) != 1 || sessions[0].Title != test.want {
					t.Fatalf("selected a different account's native history: %v, %v", sessions, err)
				}
			}
		})
	}
}

func TestEnterpriseInteractiveDefaultsKeepNativePolicyLoading(t *testing.T) {
	driverValue, err := New(harness.Config{})
	if err != nil {
		t.Fatal(err)
	}
	d := driverValue.(*driver)
	args, err := d.InteractiveArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("default startup must leave authentication, setting sources, MCP, and permission mode native: %v, %v", args, err)
	}

	// Explicit user choices are forwarded, not converted into a replacement
	// settings file or a restricted list of setting sources chosen by Atlas.
	settings := json.RawMessage(`{"permissions":{"deny":["Bash"]},"apiKeyHelper":"/company/auth-helper"}`)
	value, err := New(harness.Config{Options: map[string]json.RawMessage{
		"settings": settings, "disallowedTools": harness.JSON([]string{"WebFetch"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	args, err = value.(*driver).InteractiveArgs(nil)
	want := []string{"--disallowedTools", "WebFetch", "--settings", string(settings)}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("explicit native restrictions were changed: %v, %v", args, err)
	}
}
