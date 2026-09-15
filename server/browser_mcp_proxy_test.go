package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStripManagedProxyConfig(t *testing.T) {
	cfg := "/data/browser-mcp-proxy.json"
	args := []string{"--headless", "--config", cfg, "--isolated", "--config=" + cfg}
	got := stripManagedProxyConfig(args, cfg)
	if strings.Join(got, " ") != "--headless --isolated" {
		t.Fatalf("stripManagedProxyConfig = %v, want only user flags kept", got)
	}
	// A foreign --config is never stripped.
	foreign := []string{"--config", "/user/own.json"}
	if got := stripManagedProxyConfig(foreign, cfg); len(got) != 2 {
		t.Fatalf("foreign --config must survive: %v", got)
	}
}

func TestHasForeignProxyConfig(t *testing.T) {
	cfg := "/data/browser-mcp-proxy.json"
	if hasForeignProxyConfig([]string{"--config", cfg}, cfg) {
		t.Fatal("our own managed --config is not foreign")
	}
	if !hasForeignProxyConfig([]string{"--config", "/user/own.json"}, cfg) {
		t.Fatal("user --config must be detected as foreign")
	}
	if !hasForeignProxyConfig([]string{"--config=/user/own.json"}, cfg) {
		t.Fatal("--config= form must be detected as foreign")
	}
	if hasForeignProxyConfig([]string{"--headless"}, cfg) {
		t.Fatal("no --config at all must not be foreign")
	}
}

func TestReferencesConfig(t *testing.T) {
	cfg := "/data/browser-mcp-proxy.json"
	if !referencesConfig([]string{"--headless", "--config", cfg}, cfg) {
		t.Fatal("attached config must be referenced")
	}
	if referencesConfig([]string{"--headless"}, cfg) {
		t.Fatal("detached config must not be referenced")
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("http://artex:s3cret@127.0.0.1:8788")
	if strings.Contains(got, "s3cret") || !strings.Contains(got, "artex") {
		t.Fatalf("redactURL = %q, want password masked, user kept", got)
	}
	if got := redactURL("http://127.0.0.1:8788"); got != "http://127.0.0.1:8788" {
		t.Fatalf("redactURL without creds = %q, want unchanged", got)
	}
}

func TestWriteBrowserMCPProxyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-mcp-proxy.json")
	if err := writeBrowserMCPProxyConfig(path, "http://127.0.0.1:8788", "artex", "pw"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Browser struct {
			LaunchOptions struct {
				Proxy struct {
					Server   string `json:"server"`
					Username string `json:"username"`
					Password string `json:"password"`
				} `json:"proxy"`
			} `json:"launchOptions"`
		} `json:"browser"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Browser.LaunchOptions.Proxy
	if p.Server != "http://127.0.0.1:8788" || p.Username != "artex" || p.Password != "pw" {
		t.Fatalf("config proxy = %+v, want server/username/password", p)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("config mode=%o, want 600 (it holds the proxy password)", st.Mode().Perm())
		}
	}
}
