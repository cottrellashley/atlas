package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All values in the handoff fixture are synthetic; never inherit or print a
// developer's actual provider credentials to test forwarding.
var enterpriseVariables = map[string]string{
	"HTTPS_PROXY": "http://proxy.invalid:8443", "HTTP_PROXY": "http://proxy.invalid:8080", "NO_PROXY": "localhost,127.0.0.1",
	"NODE_EXTRA_CA_CERTS": "/fixture/company-ca.pem", "SSL_CERT_FILE": "/fixture/system-ca.pem",
	"CLAUDE_CODE_CLIENT_CERT": "/fixture/client.pem", "CLAUDE_CODE_CLIENT_KEY": "/fixture/client.key", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE": "fixture-passphrase",
	"ANTHROPIC_API_KEY": "fixture-anthropic-key", "ANTHROPIC_AUTH_TOKEN": "fixture-auth-token", "CLAUDE_CODE_OAUTH_TOKEN": "fixture-oauth-token",
	"ANTHROPIC_BASE_URL": "https://gateway.invalid", "ANTHROPIC_CUSTOM_HEADERS": "X-Fixture: synthetic",
	"AWS_PROFILE": "fixture-profile", "AWS_REGION": "us-west-2", "AWS_CONFIG_FILE": "/fixture/aws-config", "AWS_SHARED_CREDENTIALS_FILE": "/fixture/aws-credentials",
	"AWS_ACCESS_KEY_ID": "fixture-access-key", "AWS_SECRET_ACCESS_KEY": "fixture-secret", "AWS_SESSION_TOKEN": "fixture-session-token", "AWS_BEARER_TOKEN_BEDROCK": "fixture-bedrock-token",
	"GOOGLE_APPLICATION_CREDENTIALS": "/fixture/google-credentials.json", "ANTHROPIC_VERTEX_PROJECT_ID": "fixture-project", "CLOUD_ML_REGION": "us-east5",
	"ANTHROPIC_FOUNDRY_RESOURCE": "fixture-resource", "ANTHROPIC_FOUNDRY_API_KEY": "fixture-foundry-key", "ANTHROPIC_FOUNDRY_AUTH_TOKEN": "fixture-foundry-token",
	"AZURE_CLIENT_ID": "fixture-client", "AZURE_TENANT_ID": "fixture-tenant", "AZURE_CLIENT_SECRET": "fixture-client-secret",
}

func TestEnterpriseCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native handoff targets Linux/WSL")
	}
	_, source, _, _ := runtime.Caller(0)
	bin := filepath.Join(t.TempDir(), "atlas")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "./cmd/atlas")
	build.Dir = filepath.Dir(filepath.Dir(source))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	t.Run("provider network and auth environment survives handoff", func(t *testing.T) {
		for _, provider := range []string{"BEDROCK", "VERTEX", "FOUNDRY"} {
			t.Run(provider, func(t *testing.T) {
				f := newNativeCLIFixture(t, bin)
				helper, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]string{}
				for key, value := range enterpriseVariables {
					f.env = append(f.env, key+"="+value)
					want[key] = value
				}
				providerKey := "CLAUDE_CODE_USE_" + provider
				f.env = append(f.env, providerKey+"=1")
				want[providerKey] = "1"
				want["CLAUDE_CONFIG_DIR"] = f.nativeHome
				want["HTTPS_PROXY"] = "http://configured-proxy.invalid:8443"
				keys := make([]string, 0, len(want))
				for key := range want {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				f.enterpriseConfig(t, []string{helper, "-test.run=^TestEnterpriseNativeExecutable$", "--"}, map[string]string{
					"ATLAS_ENTERPRISE_HELPER": "1", "ATLAS_ENTERPRISE_KEYS": strings.Join(keys, ","), "HTTPS_PROXY": want["HTTPS_PROXY"],
				})
				login := []byte(`{"fixture":"synthetic native login sentinel"}`)
				settings := []byte(`{"permissions":{"deny":["Bash"]},"apiKeyHelper":"/fixture/auth-helper"}`)
				f.write(t, filepath.Join(f.nativeHome, ".credentials.json"), login)
				f.write(t, filepath.Join(f.nativeHome, "settings.json"), settings)
				result := f.must(t, "new", "--harness", fixtureHarness)
				var got nativeCLIObservation
				if err := json.Unmarshal(result.stdout, &got); err != nil {
					t.Fatalf("decode handoff fixture: %v", err)
				}
				if got.PID != result.pid || got.CWD != f.project || len(got.Args) != 0 {
					t.Fatal("native process/cwd handoff changed or Atlas added policy/authentication overrides")
				}
				for key, value := range want {
					if got.Environment[key] != value {
						t.Errorf("native environment changed %s", key)
					}
				}
				f.assertFile(t, filepath.Join(f.nativeHome, ".credentials.json"), login)
				f.assertFile(t, filepath.Join(f.nativeHome, "settings.json"), settings)
				f.assertNoManagedState(t)
				shown := f.must(t, "config", "show")
				if bytes.Contains(shown.stdout, []byte(want["HTTPS_PROXY"])) {
					t.Fatal("config show exposed configured environment values")
				}
			})
		}
	})

	t.Run("installed Claude uses isolated native auth context", func(t *testing.T) {
		claude := enterpriseClaude(t)
		f := newNativeCLIFixture(t, bin)
		f.enterpriseConfig(t, []string{claude}, nil)
		f.enterpriseNativeEnvironment()
		result := f.must(t, "exec", "--harness", fixtureHarness, "--", "auth", "status", "--json")
		var status struct {
			LoggedIn          bool   `json:"loggedIn"`
			AuthMethod        string `json:"authMethod"`
			APIKeySource      string `json:"apiKeySource"`
			ConfigDirectory   string `json:"configDirectory"`
			ProjectsDirectory string `json:"projectsDirectory"`
		}
		if err := json.Unmarshal(result.stdout, &status); err != nil {
			t.Fatalf("native auth status: %v", err)
		}
		if !status.LoggedIn || status.AuthMethod != "api_key" || status.APIKeySource != "ANTHROPIC_API_KEY" || status.ConfigDirectory != f.nativeHome || status.ProjectsDirectory != filepath.Join(f.nativeHome, "projects") {
			t.Fatalf("native auth selected an unexpected source: %#v", status)
		}
		if bytes.Contains(result.stdout, []byte("fixture-native-api-key")) {
			t.Fatal("native auth status exposed the synthetic API key")
		}
		f.assertNoManagedState(t)
	})

	t.Run("installed managed policy cannot be relaxed through Atlas", func(t *testing.T) {
		claude := enterpriseClaude(t)
		if runtime.GOOS != "linux" {
			t.Skip("isolated /etc managed-policy fixture requires Linux bubblewrap")
		}
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			t.Skip("bubblewrap unavailable; real system policy is never modified")
		}
		probe := exec.Command(bwrap, "--ro-bind", "/", "/", "--unshare-net", "--", "/usr/bin/true")
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("private mount/network namespace unavailable: %v (%s)", err, out)
		}
		f := newNativeCLIFixture(t, bin)
		f.enterpriseConfig(t, []string{claude}, nil)
		f.enterpriseNativeEnvironment()
		f.write(t, filepath.Join(f.nativeHome, "settings.json"), []byte(`{"enabledMcpjsonServers":["enterprise-blocked"]}`))
		f.write(t, filepath.Join(f.project, ".mcp.json"), []byte(`{"mcpServers":{"enterprise-blocked":{"type":"http","url":"http://127.0.0.1:1/mcp"}}}`))
		policy := filepath.Join(f.root, "managed-settings.json")
		invoke := func(args ...string) ([]byte, error) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			argv := []string{"--ro-bind", "/", "/", "--bind", f.root, f.root, "--tmpfs", "/etc", "--dir", "/etc/claude-code", "--ro-bind", policy, "/etc/claude-code/managed-settings.json", "--unshare-net", "--die-with-parent", "--chdir", f.project, "--", bin, "exec", "--harness", fixtureHarness, "--"}
			cmd := exec.CommandContext(ctx, bwrap, append(argv, args...)...)
			cmd.Env = f.env
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("native policy probe timed out: %s", out)
			}
			return out, err
		}
		// A project server is visible without the policy. The isolated network
		// has no listener, so this does not contact any external service.
		f.write(t, policy, []byte(`{}`))
		baseline, _ := invoke("mcp", "get", "enterprise-blocked")
		if !bytes.Contains(baseline, []byte("http://127.0.0.1:1/mcp")) {
			t.Fatalf("baseline native project MCP was not loaded: %s", baseline)
		}
		blockedPolicy := []byte(`{"deniedMcpServers":[{"serverName":"enterprise-blocked"}]}`)
		f.write(t, policy, blockedPolicy)
		out, err := invoke("--settings", `{"deniedMcpServers":[],"allowedMcpServers":[{"serverName":"enterprise-blocked"}]}`, "mcp", "get", "enterprise-blocked")
		if err == nil || !bytes.Contains(out, []byte(`No MCP server named "enterprise-blocked"`)) {
			t.Fatalf("managed MCP restriction was relaxed by command-line settings: error=%v output=%s", err, out)
		}
		f.assertFile(t, policy, blockedPolicy)
		f.write(t, policy, []byte(`{malformed-policy`))
		out, err = invoke("doctor")
		if err == nil || !bytes.Contains(out, []byte("Managed settings document could not be parsed")) {
			t.Fatalf("invalid managed policy did not fail closed: error=%v output=%s", err, out)
		}
		f.assertNoManagedState(t)
	})

	t.Run("installed MCP keeps cloud credentials out of project headers", func(t *testing.T) {
		claude := enterpriseClaude(t)
		f := newNativeCLIFixture(t, bin)
		f.enterpriseConfig(t, []string{claude}, nil)
		f.enterpriseNativeEnvironment()
		f.env = append(f.env, "ATLAS_ENTERPRISE_MCP_TOKEN=fixture-service-token", "AWS_SECRET_ACCESS_KEY=fixture-aws-secret", "ANTHROPIC_FOUNDRY_API_KEY=fixture-foundry-key")
		var initialized, badHeaders atomic.Int64
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Authorization") != "Bearer fixture-service-token" || r.Header.Get("X-Anthropic") != "" || r.Header.Get("X-Aws") != "" || r.Header.Get("X-Foundry") != "" {
				badHeaders.Add(1)
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(req.ID) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			var result any = map[string]any{}
			if req.Method == "initialize" {
				initialized.Add(1)
				result = map[string]any{"protocolVersion": req.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "enterprise-fixture", "version": "1"}}
			} else if req.Method == "tools/list" {
				result = map[string]any{"tools": []any{}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}))
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.StartTLS()
		defer server.Close()
		mcp := map[string]any{"mcpServers": map[string]any{"enterprise-fixture": map[string]any{"type": "http", "url": server.URL, "headers": map[string]string{"Authorization": "Bearer ${ATLAS_ENTERPRISE_MCP_TOKEN}", "X-Anthropic": "${ANTHROPIC_API_KEY}", "X-Aws": "${AWS_SECRET_ACCESS_KEY}", "X-Foundry": "${ANTHROPIC_FOUNDRY_API_KEY}"}}}}
		data, _ := json.Marshal(mcp)
		f.write(t, filepath.Join(f.project, ".mcp.json"), data)
		pending := f.must(t, "exec", "--harness", fixtureHarness, "--", "mcp", "list")
		if initialized.Load() != 0 || !bytes.Contains(pending.stdout, []byte("Pending approval")) {
			t.Fatalf("unapproved project MCP was connected: requests=%d output=%s", initialized.Load(), pending.stdout)
		}
		// This approval belongs solely to the disposable test profile.
		f.write(t, filepath.Join(f.nativeHome, "settings.json"), []byte(`{"enabledMcpjsonServers":["enterprise-fixture"]}`))
		untrusted := f.must(t, "exec", "--harness", fixtureHarness, "--", "mcp", "list")
		if initialized.Load() != 0 || !bytes.Contains(untrusted.stdout, []byte("Failed")) {
			t.Fatalf("untrusted TLS fixture was not rejected: requests=%d output=%s", initialized.Load(), untrusted.stdout)
		}
		ca := filepath.Join(f.root, "fixture-ca.pem")
		f.write(t, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
		f.env = append(f.env, "NODE_EXTRA_CA_CERTS="+ca)
		connected := f.must(t, "exec", "--harness", fixtureHarness, "--", "mcp", "list")
		if initialized.Load() == 0 || badHeaders.Load() != 0 || bytes.Contains(connected.stdout, []byte("Failed")) {
			t.Fatalf("native MCP authentication isolation failed: initializations=%d invalidHeaders=%d output=%s", initialized.Load(), badHeaders.Load(), connected.stdout)
		}
		f.assertFile(t, filepath.Join(f.project, ".mcp.json"), data)
		f.assertNoManagedState(t)
	})
}

func enterpriseClaude(t *testing.T) string {
	t.Helper()
	if os.Getenv("ATLAS_CLAUDE_ENTERPRISE") != "1" {
		t.Skip("set ATLAS_CLAUDE_ENTERPRISE=1 for isolated installed-Claude enterprise checks (no inference)")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(binary, "/mise/shims/") {
		out, err := exec.Command("mise", "which", "claude").Output()
		if err != nil {
			t.Fatal(err)
		}
		binary = strings.TrimSpace(string(out))
	}
	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(string(out)))
	return binary
}

func (f *nativeCLIFixture) enterpriseConfig(t *testing.T, command []string, overrides map[string]string) {
	t.Helper()
	env := map[string]string{"CLAUDE_CONFIG_DIR": f.nativeHome}
	for key, value := range overrides {
		env[key] = value
	}
	data, err := json.Marshal(map[string]any{"schemaVersion": 1, "harnesses": map[string]any{fixtureHarness: map[string]any{"adapter": "claude", "command": command, "env": env}}})
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join(f.home, "config.json"), data)
}

func (f *nativeCLIFixture) enterpriseNativeEnvironment() {
	f.env = append(f.env, "PATH=/usr/bin:/bin", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "ANTHROPIC_API_KEY=fixture-native-api-key", "ANTHROPIC_BASE_URL=http://127.0.0.1:1", "NO_COLOR=1", "NO_PROXY=127.0.0.1,localhost", "TMPDIR="+f.root)
}

func TestEnterpriseNativeExecutable(t *testing.T) {
	if os.Getenv("ATLAS_ENTERPRISE_HELPER") != "1" {
		t.Skip("native environment subprocess fixture")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	env := map[string]string{}
	for _, key := range strings.Split(os.Getenv("ATLAS_ENTERPRISE_KEYS"), ",") {
		env[key] = os.Getenv(key)
	}
	if err := json.NewEncoder(os.Stdout).Encode(nativeCLIObservation{Args: args, CWD: cwd, PID: os.Getpid(), Environment: env}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
