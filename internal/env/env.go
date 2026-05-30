// Package env loads .env and .env.local into the process environment for the
// kobolt commands.
//
// It replaces the bare github.com/joho/godotenv/autoload import, which reads
// only .env and only relative to the process working directory. That is fine
// in an interactive shell (where direnv also loads .env.local), but breaks
// under cron: .env.local is never read and the cwd is $HOME, not the repo.
//
// Load resolves the files from the module root (derived from the executable,
// or the MODULE_ROOT override) so binaries work from any directory, and it
// loads .env.local as well as .env.
package env

import (
	"path/filepath"

	"github.com/gavincarr/kobolt/internal/moduleroot"
	"github.com/joho/godotenv"
)

// Load reads .env.local then .env into the process environment. The files are
// resolved from the module root when it can be derived from the executable;
// otherwise they are read relative to the current directory.
//
// godotenv.Load never overwrites a variable that is already set, so the
// precedence is: real environment > .env.local > .env. This matches the
// .envrc direnv ordering. Missing files are ignored.
func Load() {
	var dir string
	if root, err := moduleroot.Root(); err == nil {
		dir = root
	}
	for _, name := range []string{".env.local", ".env"} {
		_ = godotenv.Load(filepath.Join(dir, name))
	}
}
