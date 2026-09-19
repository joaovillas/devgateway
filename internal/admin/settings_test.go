package admin

import (
	"slices"
	"testing"
)

func TestTouchedKeys(t *testing.T) {
	for _, c := range []struct {
		patch map[string]any
		want  []string
	}{
		{map[string]any{"seed": 1}, []string{"seed"}},
		{map[string]any{"ports": map[string]any{"traffic": 9090}}, []string{"ports.traffic"}},
		{map[string]any{"ports": nil}, []string{"ports.admin", "ports.traffic"}},
		{map[string]any{"ports": map[string]any{}}, nil},
		{map[string]any{"history": map[string]any{"backend": "sqlite", "path": nil}}, []string{"history.backend", "history.path"}},
		{map[string]any{"schemaVersion": 1, "unknown": 2}, nil},
	} {
		if got := touchedKeys(c.patch, ""); !slices.Equal(got, c.want) {
			t.Errorf("%v: %v, want %v", c.patch, got, c.want)
		}
	}
}
