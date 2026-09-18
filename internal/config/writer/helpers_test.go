package writer

import (
	"os"
	"testing"
	"time"
)

// tempDir é um t.TempDir cuja remoção insiste por alguns segundos. No
// Windows, um arquivo recém-substituído por rename pode ficar marcado para
// exclusão enquanto outro processo (o antivírus) ainda o mantém aberto, e o
// diretório só esvazia quando ele o solta.
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
				t.Errorf("removendo o diretório temporário: %v", err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}
