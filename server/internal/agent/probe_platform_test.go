package agent

import "testing"

func TestBackgroundRuntimeProbeEnabledOnAllPlatforms(t *testing.T) {
	// v0.4.4 disabled background runtime probing on Windows because the claude/
	// codex SDKs could not inject CREATE_NO_WINDOW and would flash a blank
	// console window. Those SDKs now attach HideWindow + CREATE_NO_WINDOW, so the
	// gate was removed and probing runs on every platform. Verify that no OS is
	// skipped, or an agent's status stays frozen at "probe pending".
	for _, goos := range []string{"windows", "darwin", "linux"} {
		if !shouldRunBackgroundRuntimeProbe(goos) {
			t.Fatalf("%s should keep background runtime probes enabled", goos)
		}
	}
}
