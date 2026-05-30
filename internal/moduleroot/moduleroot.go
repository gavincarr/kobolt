// Internal module to identify the root path of a module
package moduleroot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root returns the root path of the module derived from the executable path
// or the MODULE_ROOT environment variable, or an error.
func Root() (string, error) {
	root := os.Getenv("MODULE_ROOT")
	if root != "" {
		return root, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	bindir := filepath.Dir(path)
	if strings.HasSuffix(bindir, "/bin") {
		return filepath.Join(bindir, ".."), nil
	}
	filename := filepath.Base(path)
	if strings.HasSuffix(bindir, "/cmd/"+filename) {
		return filepath.Join(bindir, "../.."), nil
	}
	return "", fmt.Errorf("unable to determine root from bindir %q", bindir)
}
