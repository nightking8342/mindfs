package main

import (
	"flag"
	"io"
	"mindfs/server/app"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalHTTPCommandsDiscoverTransport(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "http"
		if useTLS {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(internalRestartEnvKey, "")
			t.Setenv(daemonEnvKey, "")
			for _, key := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			var token string
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (r.TLS != nil) != useTLS {
					t.Error("incorrect transport")
				}
				if r.URL.Path != "/health" && r.Header.Get("X-MindFS-Local-CLI-Token") != token {
					http.Error(w, "local CLI token required", http.StatusUnauthorized)
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /health":
					io.WriteString(w, `{}`)
				case "POST /api/dirs":
					io.WriteString(w, `{"id":"root","root_path":"/project"}`)
				case "DELETE /api/dirs":
					if r.URL.Query().Get("path") != "/project" {
						t.Error("missing directory path")
					}
					w.WriteHeader(http.StatusNoContent)
				case "GET /api/relay/status", "POST /api/relay/bind/start":
					io.WriteString(w, `{"relay_bound":true,"node_url":"https://relay.example/n/node"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			if useTLS {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			addr := server.Listener.Addr().String()
			var err error
			token, err = app.EnsureLocalCLIToken(addr, useTLS)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.EnsureE2EEConfig(true); err != nil {
				t.Fatal(err)
			}
			configDir, err := os.UserConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			e2eePath := filepath.Join(configDir, "mindfs", "e2ee.json")
			originalE2EE, err := os.ReadFile(e2eePath)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise main's service reuse and remove dispatch without -tls.
			for _, args := range [][]string{
				{"-addr", addr, "-bind-relay", "/project"},
				{"-addr", addr, "-remove", "/project"},
			} {
				cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestLocalCLICommandProcess$", "--"}, args...)...)
				cmd.Env = append(os.Environ(), "MINDFS_TEST_LOCAL_CLI_PROCESS=1")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("CLI %v failed: %v\n%s", args, err, output)
				}
			}
			if after, err := os.ReadFile(e2eePath); err != nil || string(after) != string(originalE2EE) {
				t.Fatalf("reusing service changed E2EE settings: %v", err)
			}
			for _, target := range []string{addr, server.URL + "/"} {
				clientTLS, err := resolveClientTLS(target, false, false)
				if err != nil || clientTLS != useTLS {
					t.Fatalf("TLS = %v, %v", clientTLS, err)
				}
				if !serverRunning(target, clientTLS) {
					t.Fatal("running server reported stopped")
				}
				if dir, err := addManagedDir(target, clientTLS, "/project"); err != nil || dir.ID != "root" {
					t.Fatalf("add directory: %+v, %v", dir, err)
				}
				if err := handleRemoveRoot(target, clientTLS, "/project"); err != nil {
					t.Fatal(err)
				}
				for _, fetch := range []func(string, bool) (relayStatusResponse, error){fetchRelayStatus, startRelayBinding} {
					if status, err := fetch(target, clientTLS); err != nil || !status.Bound || status.NodeURL == "" {
						t.Fatalf("relay status: %+v, %v", status, err)
					}
				}
			}
		})
	}
}

func TestLocalCLICommandProcess(t *testing.T) {
	if os.Getenv("MINDFS_TEST_LOCAL_CLI_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"mindfs"}, os.Args[i+1:]...)
			flag.CommandLine = flag.NewFlagSet("mindfs", flag.ExitOnError)
			main()
			return
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestResolveClientTLSExplicitScheme(t *testing.T) {
	for _, addr := range []string{"https://127.0.0.1:7331", "http://127.0.0.1:7331"} {
		want := strings.HasPrefix(addr, "https://")
		if got, err := resolveClientTLS(addr, !want, true); err != nil || got != want {
			t.Fatalf("%s: TLS = %v, %v", addr, got, err)
		}
	}
}

func TestAddrToURLIPv6(t *testing.T) {
	for addr, want := range map[string]string{
		"[::1]:7331": "https://[::1]:7331/health",
		"[::]:7331":  "https://127.0.0.1:7331/health",
		":7331":      "https://localhost:7331/health",
	} {
		if got := addrToURL(addr, "/health", true); got != want {
			t.Fatalf("%s: got %s, want %s", addr, got, want)
		}
	}
}
