package web

import (
	"io/fs"
	"testing"
)

// TestEmbeddedAssetsPresent guards against a broken go:embed pattern
// silently shipping a binary with no frontend.
func TestEmbeddedAssetsPresent(t *testing.T) {
	for _, name := range []string{"index.html", "script.js", "styles.css"} {
		if _, err := fs.Stat(Static(), name); err != nil {
			t.Errorf("%s missing from embedded FS: %v", name, err)
		}
	}
}
