package app

import (
	"os"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
)

func loaderFor(path string) config.Loader {
	return config.Loader{ConfigPath: path, Getenv: os.LookupEnv}
}

// tempDir is a t.TempDir whose removal keeps retrying for a few seconds. On
// Windows, a file just replaced by a rename can stay marked for deletion
// while another process (the antivirus) still holds it open, and the
// directory only empties once that process lets go.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gateway-test-")
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
