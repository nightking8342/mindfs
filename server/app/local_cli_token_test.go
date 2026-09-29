package app

import (
	"encoding/json"
	"os"
	"testing"

	"mindfs/server/internal/testutil"
)

func TestLocalCLITokenStoreKeepsTokensByAddress(t *testing.T) {
	setTestConfigHome(t, t.TempDir())

	first, err := EnsureLocalCLIToken("127.0.0.1:7331", true)
	if err != nil {
		t.Fatalf("EnsureLocalCLIToken first: %v", err)
	}
	second, err := EnsureLocalCLIToken("127.0.0.1:9000", false)
	if err != nil {
		t.Fatalf("EnsureLocalCLIToken second: %v", err)
	}
	if first == second {
		t.Fatal("expected different tokens for different addresses")
	}

	gotFirst, err := ReadLocalCLIToken(":7331")
	if err != nil {
		t.Fatalf("ReadLocalCLIToken first: %v", err)
	}
	if gotFirst != first {
		t.Fatalf("first token = %q, want %q", gotFirst, first)
	}
	gotSecond, err := ReadLocalCLIToken("127.0.0.1:9000")
	if err != nil {
		t.Fatalf("ReadLocalCLIToken second: %v", err)
	}
	if gotSecond != second {
		t.Fatalf("second token = %q, want %q", gotSecond, second)
	}
	for addr, want := range map[string]bool{":7331": true, "127.0.0.1:9000": false} {
		if got, err := ReadLocalCLITLS(addr); err != nil || got != want {
			t.Fatalf("TLS for %s = %v, %v; want %v", addr, got, err, want)
		}
	}
	if _, err := EnsureLocalCLIToken(":7331", false); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLocalCLITLS(":7331"); err != nil || got {
		t.Fatalf("TLS after HTTP restart = %v, %v", got, err)
	}
}

func TestLocalCLITLSLegacyStore(t *testing.T) {
	setTestConfigHome(t, t.TempDir())
	path, err := localCLITokenStorePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalCLITokenStore(path, localCLITokenStore{Tokens: map[string]string{"127.0.0.1:7331": "legacy"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLocalCLITLS(":7331"); err != nil || got {
		t.Fatalf("legacy TLS = %v, %v", got, err)
	}
	if token, err := ReadLocalCLIToken(":7331"); err != nil || token != "legacy" {
		t.Fatalf("legacy token = %q, %v", token, err)
	}
}

func TestLocalCLITokenStoreWritesSinglePrivateFile(t *testing.T) {
	configRoot := t.TempDir()
	setTestConfigHome(t, configRoot)

	if _, err := EnsureLocalCLIToken(":7331", false); err != nil {
		t.Fatalf("EnsureLocalCLIToken: %v", err)
	}
	path, err := localCLITokenStorePath()
	if err != nil {
		t.Fatalf("localCLITokenStorePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token store mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token store: %v", err)
	}
	var store localCLITokenStore
	if err := json.Unmarshal(raw, &store); err != nil {
		t.Fatalf("unmarshal token store: %v", err)
	}
	if len(store.Tokens) != 1 {
		t.Fatalf("token count = %d, want 1", len(store.Tokens))
	}
}

func setTestConfigHome(t *testing.T, dir string) {
	t.Helper()
	testutil.IsolateUserDirs(t, dir)
}
