package config

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// consistentSnapshot cria um snapshot cujas partes precisam concordar entre si:
// o seed, a porta, a quantidade de rotas e o índice por nome codificam n.
func consistentSnapshot(t *testing.T, n int) *Snapshot {
	t.Helper()
	var docs []RouteDoc
	for i := range n {
		name := fmt.Sprintf("r%d", i)
		docs = append(docs, NewRouteDoc(name+".yaml", Route{
			SchemaVersion: 1,
			Name:          name,
			Upstream:      "http://localhost:9000",
			Match:         RouteMatch{Path: fmt.Sprintf("/r%d/*", i)},
		}))
	}
	routes, err := BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(n)
	return NewSnapshot(Settings{Seed: &seed, TrafficPort: 10000 + n}, routes, nil)
}

func checkConsistent(s *Snapshot) error {
	n := int(*s.Settings.Seed)
	if s.Settings.TrafficPort != 10000+n || len(s.Routes) != n || len(s.byName) != n {
		return fmt.Errorf("snapshot parcial: seed %d, porta %d, rotas %d, índice %d",
			n, s.Settings.TrafficPort, len(s.Routes), len(s.byName))
	}
	for _, r := range s.Routes {
		if s.Route(r.Name()) != r {
			return fmt.Errorf("índice por nome não corresponde à lista em %s", r.Name())
		}
	}
	return nil
}

func TestLiveSwapNeverExposesPartialState(t *testing.T) {
	snaps := make([]*Snapshot, 8)
	for i := range snaps {
		snaps[i] = consistentSnapshot(t, i+1)
	}
	live := NewLive(snaps[0])

	var stop atomic.Bool
	var reads atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 8 {
		wg.Go(func() {
			for !stop.Load() {
				// Uma requisição captura o ponteiro uma vez e lê tudo dele.
				if err := checkConsistent(live.Load()); err != nil {
					errs <- err
					return
				}
				reads.Add(1)
			}
		})
	}
	for i := range 2000 {
		live.Swap(snaps[i%len(snaps)])
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reads.Load() == 0 {
		t.Fatal("nenhuma leitura concorrente ocorreu")
	}
}

func TestLoaderBuildsSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"gateway.json":     `{"routesDir":"routes"}`,
		"routes/a.yaml":    routeYAML("a", "/a/*"),
		"routes/b.yaml":    routeYAML("b", "/b/*"),
		"routes/leia.txt":  "ignorado",
		"routes/c.yaml":    routeYAML("c", "/c"),
		"routes/old/x.yml": "ignorado: [",
	})
	snap, err := Loader{ConfigPath: dir + "/gateway.json", Getenv: envMap(nil)}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Routes) != 3 || snap.Route("b") == nil {
		t.Fatalf("snapshot deveria ter as rotas a, b e c: %d", len(snap.Routes))
	}
}
