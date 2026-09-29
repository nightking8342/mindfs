package codex

import (
	"context"
	codexsdk "github.com/fanwenlin/codex-go-sdk/codex"
	"os"
	"testing"
	"time"
)

// Opt-in integration check; only forks existing history, without starting a turn.
func TestForkCodexThreadLive(t *testing.T) {
	sourceID := os.Getenv("MINDFS_TEST_CODEX_FORK_SOURCE")
	if sourceID == "" {
		t.Skip("set MINDFS_TEST_CODEX_FORK_SOURCE to test the installed app server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := newClient(OpenOptions{Command: "codex"})
	defer client.Close()
	ordinal := 1
	id, err := forkCodexThread(ctx, client, codexsdk.ThreadOptions{}, sourceID, &ordinal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := client.AppServerRPC(ctx, "thread/archive", map[string]any{"threadId": id}); err != nil {
			t.Errorf("archive test fork: %v", err)
		}
	}()
	var result struct {
		Thread struct {
			Turns []struct {
				ID string `json:"id"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := client.AppServerRPCTyped(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Thread.Turns) != 1 {
		t.Fatalf("fork has %d turns, want 1", len(result.Thread.Turns))
	}
}
