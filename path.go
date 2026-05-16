package kobolt

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// OutputPath returns the dated snapshot path next to the input file:
// <input-no-ext>_<YYYYMMDD>.json in the input's directory.
func OutputPath(inputFile string, t time.Time) string {
	dir := filepath.Dir(inputFile)
	base := filepath.Base(inputFile)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, fmt.Sprintf("%s_%s.json", name, t.Format("20060102")))
}
