package config

import (
	"encoding/json"
	"fmt"
	"github.com/cottrellashley/atlas/internal/harness"
	"os"
	"path/filepath"
	"regexp"
)

type Config struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Harnesses     map[string]harness.Config `json:"harnesses"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func Home() (string, error) {
	if p := os.Getenv("ATLAS_HOME"); p != "" {
		return filepath.Abs(p)
	}
	h, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, ".atlas"), nil
}
func Load(home string) (Config, error) {
	c := Config{SchemaVersion: 1, Harnesses: map[string]harness.Config{}}
	for _, id := range []string{"opencode", "pi", "claude"} {
		c.Harnesses[id] = harness.Config{ID: id, Adapter: id, Command: []string{id}}
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	var user Config
	if err = json.Unmarshal(data, &user); err != nil {
		return c, fmt.Errorf("config.json: %w", err)
	}
	if user.SchemaVersion != 1 {
		return c, fmt.Errorf("config schemaVersion must be 1")
	}
	for id, h := range user.Harnesses {
		if !identifier.MatchString(id) {
			return c, fmt.Errorf("invalid harness ID %q", id)
		}
		if h.Adapter == "" {
			h.Adapter = id
		}
		switch h.Adapter {
		case "pi", "opencode", "claude":
		default:
			return c, fmt.Errorf("unknown adapter %q", h.Adapter)
		}
		if len(h.Command) == 0 {
			h.Command = []string{h.Adapter}
		}
		if h.Command[0] == "" {
			return c, fmt.Errorf("empty command for %s", id)
		}
		h.ID = id
		c.Harnesses[id] = h
	}
	return c, nil
}
func (c Config) Resolve(id string) (harness.Config, error) {
	h, ok := c.Harnesses[id]
	if !ok {
		return h, fmt.Errorf("unknown harness %q; use atlas harnesses", id)
	}
	return h, nil
}
