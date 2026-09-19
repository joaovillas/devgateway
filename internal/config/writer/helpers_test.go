package writer

import (
	"os"
	"testing"
	"time"
)

// tempDir is a t.TempDir whose removal keeps trying for a few seconds. On
// Windows, a file that was just replaced by a rename can stay marked for
// deletion while another process (the antivirus) still holds it open, and the
// directory only empties once that process lets go.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gateway-writer-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("removing the temporary directory: %v", err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}
