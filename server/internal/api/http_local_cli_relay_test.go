package api

import (
	"encoding/json"
	"mindfs/server/internal/e2ee"
	"mindfs/server/internal/relay"
	"mindfs/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalCLIRelayStatusWithE2EE(t *testing.T) {
	testutil.IsolateUserDirs(t, t.TempDir())
	t.Setenv("MINDFS_RELAY_BASE_URL", "")
	store, err := relay.NewCredentialsStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(relay.Credentials{Relay: relay.RelayCredentials{
		DeviceToken: "test-device", NodeID: "node", Endpoint: "https://relay.example.com",
	}}); err != nil {
		t.Fatal(err)
	}
	manager, err := relay.NewManager(":7331", false, "https://relay.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	h := &HTTPHandler{
		LocalCLIToken: "test-cli",
		AppContext: &AppContext{Relay: manager, E2EE: e2ee.NewManager(e2ee.Config{
			Enabled: true, NodeID: "e2ee-node", PairingSecret: "secret",
		})},
	}
	for _, tc := range []struct {
		name, remote, token string
		full                bool
	}{
		{"local", "127.0.0.1:1234", "test-cli", true},
		{"ipv6", "[::1]:1234", "test-cli", true},
		{"remote", "192.0.2.1:1234", "test-cli", false},
		{"invalid token", "127.0.0.1:1234", "wrong", false},
		{"anonymous", "127.0.0.1:1234", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/relay/status", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set(localCLIHeaderName, tc.token)
			w := httptest.NewRecorder()
			h.handleRelayStatus(w, r)
			var status relay.Status
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if !status.E2EERequired || w.Header().Get(e2eeHeaderName) != "" {
				t.Fatal("expected plaintext status with E2EE enabled")
			}
			if tc.full {
				if !status.Bound || status.NodeID != "node" || status.NodeURL == "" {
					t.Fatalf("missing local status: %+v", status)
				}
			} else if status.Bound || status.NodeID != "" || status.NodeURL != "" || status.RelayBaseURL != "" {
				t.Fatalf("sensitive status leaked: %+v", status)
			}
		})
	}
}
