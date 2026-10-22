package omp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func (d *Driver) lookupEnv(key string) (string, bool) {
	if value, ok := d.cfg.Env[key]; ok {
		return value, true
	}
	return os.LookupEnv(key)
}

func (d *Driver) env(key string) string { value, _ := d.lookupEnv(key); return value }

func (d *Driver) home() string {
	if home := d.env("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

func (d *Driver) commandFlag(name string) (string, bool, error) {
	var value string
	var found bool
	for i := 1; i < len(d.cfg.Command); i++ {
		arg := d.cfg.Command[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, name+"=") {
			value, found = strings.TrimPrefix(arg, name+"="), true
		} else if arg == name {
			if i+1 == len(d.cfg.Command) || strings.HasPrefix(d.cfg.Command[i+1], "--") {
				return "", false, fmt.Errorf("OMP command %s requires a value", name)
			}
			i++
			value, found = d.cfg.Command[i], true
		}
	}
	return value, found, nil
}

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var reservedProfile = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(\..*)?$`)

func normalizeProfile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "default" {
		return "", nil
	}
	if !profileName.MatchString(value) || strings.HasSuffix(value, ".") || reservedProfile.MatchString(value) {
		return "", fmt.Errorf("invalid OMP profile %q", value)
	}
	return value, nil
}

func (d *Driver) profile() (string, error) {
	value, found, err := d.commandFlag("--profile")
	if err != nil {
		return "", err
	}
	if d.opts.Profile != nil {
		if found && strings.TrimSpace(value) != strings.TrimSpace(*d.opts.Profile) {
			return "", fmt.Errorf("OMP profile differs between command and options")
		}
		value, found = *d.opts.Profile, true
	}
	if !found {
		if canonical, defined := d.lookupEnv("OMP_PROFILE"); defined {
			value = canonical
		} else {
			value = d.env("PI_PROFILE")
		}
	}
	return normalizeProfile(value)
}

func resolvePath(value, cwd string) (string, error) {
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(filepath.Join(cwd, value))
}

// sessionsRoot mirrors OMP v18's getSessionsDir/DirResolver. Native config
// files do not define sessionDir; that is a CLI flag or environment override.
func (d *Driver) sessionsRoot(cwd string) (root string, explicit bool, err error) {
	configured, found, err := d.commandFlag("--session-dir")
	if err != nil {
		return "", false, err
	}
	if d.opts.SessionDir != "" {
		if found && configured != d.opts.SessionDir {
			return "", false, fmt.Errorf("OMP sessionDir differs between command and options")
		}
		configured = d.opts.SessionDir
	}
	if configured == "" {
		configured = d.env("PI_CODING_AGENT_SESSION_DIR")
	}
	if configured != "" {
		root, err = resolvePath(configured, cwd)
		return root, true, err
	}
	profile, err := d.profile()
	if err != nil {
		return "", false, err
	}
	configName := d.env("PI_CONFIG_DIR")
	if configName == "" {
		configName = ".omp"
	}
	configRoot := filepath.Join(d.home(), configName)
	if profile != "" {
		configRoot = filepath.Join(configRoot, "profiles", profile)
	}
	agentDir := filepath.Join(configRoot, "agent")
	if override := d.env("PI_CODING_AGENT_DIR"); profile == "" && override != "" {
		// An inherited profile-derived value is not a default-profile override.
		legacy, _ := normalizeProfile(d.env("PI_PROFILE"))
		derived := filepath.Join(d.home(), configName, "profiles", legacy, "agent")
		if legacy == "" || filepath.Clean(override) != derived {
			resolved, err := resolvePath(override, cwd)
			if err != nil {
				return "", false, err
			}
			if resolved != agentDir {
				return filepath.Join(resolved, "sessions"), false, nil
			}
		}
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if dataHome := d.env("XDG_DATA_HOME"); dataHome != "" {
			base := filepath.Join(dataHome, "omp")
			if profile != "" {
				base = filepath.Join(base, "profiles", profile)
			}
			if _, err := os.Stat(base); err == nil {
				return filepath.Join(base, "sessions"), false, nil
			}
		}
	}
	return filepath.Join(agentDir, "sessions"), false, nil
}

func canonicalPath(value string) string {
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return filepath.Clean(value)
}

func beneath(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	return relative, err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (d *Driver) projectSessionDir(cwd string) (string, error) {
	root, explicit, err := d.sessionsRoot(cwd)
	if err != nil || explicit {
		return root, err
	}
	cwd, err = resolvePath(cwd, "")
	if err != nil {
		return "", err
	}
	cwd = canonicalPath(cwd)
	temp := d.env("TMPDIR")
	if temp == "" {
		temp = "/tmp"
	}
	encode := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace
	var bucket string
	if relative, ok := beneath(canonicalPath(temp), cwd); ok {
		bucket = "-tmp"
		if relative != "." {
			bucket += "-" + encode(relative)
		}
	} else if relative, ok := beneath(canonicalPath(d.home()), cwd); ok {
		bucket = "-"
		if relative != "." {
			bucket += encode(relative)
		}
	} else {
		bucket = "--" + encode(strings.TrimPrefix(cwd, string(filepath.Separator))) + "--"
	}
	return filepath.Join(root, bucket), nil
}
