package app

import (
	"os"
	"path/filepath"

	"mindfs/server/internal/config"
)

// DefaultStartupConfigPath returns the conventional path of the startup config
// file (the one normally passed via -config) and whether a file already exists
// there.
//
// fork: upstream's startup config has no default location — `-config` must be
// passed explicitly, and when it is omitted the CLI silently falls back to its
// hardcoded defaults (addr 127.0.0.1:7331). That breaks any deployment whose
// service runs on a different address: a task agent reporting with `-from-task`
// (which never passes -config) probes 7331, finds nothing, and — because the
// CLI starts a service when none is listening — spawns a second, empty instance
// to report into. Observed in the vFlow project's task groups.
//
// The CLI (cli/cmd) cannot import server/internal/config directly, so this
// helper bridges it. Location matches the runtime data directory used by
// everything else (registry.json, task_template.json, local-cli-tokens.json).
func DefaultStartupConfigPath() (string, bool) {
	dir, err := config.MindFSConfigDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}
