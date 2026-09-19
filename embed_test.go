package devgateway

import (
	"io/fs"
	"testing"
)

func TestWebFSServesIndex(t *testing.T) {
	if _, err := fs.Stat(WebFS(), "index.html"); err != nil {
		t.Fatalf("index.html ausente do frontend embutido: %v", err)
	}
}
