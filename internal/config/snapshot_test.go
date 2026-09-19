package config

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// consistentSnapshot builds a snapshot whose parts have to agree with each
// other: the seed, the port, the number of routes and the index by name all
// encode n.
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
		return fmt.Errorf("partial snapshot: seed %d, port %d, routes %d, index %d",
			n, s.Settings.TrafficPort, len(s.Routes), len(s.byName))
	}
	for _, r := range s.Routes {
		if s.Route(r.Name()) != r {
			return fmt.Errorf("the index by name does not match the list at %s", r.Name())
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
				// A request grabs the pointer once and reads everything from it.
				if err := checkConsistent(live.Load()); err != nil {
					errs <- err
					return
				}
				reads.Add(1)
			}
		})
	}
	// Keep swapping until at least one concurrent read has happened: on a
	// machine with few cores the 2000 swaps may finish before any reader
	// gets going.
	for i := 0; i < 2000 || reads.Load() == 0; i++ {
		live.Swap(snaps[i%len(snaps)])
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reads.Load() == 0 {
		t.Fatal("no concurrent read happened")
	}
}

func TestLoaderBuildsSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"gateway.json":      `{"routesDir":"routes"}`,
		"routes/a.yaml":     routeYAML("a", "/a/*"),
		"routes/b.yaml":     routeYAML("b", "/b/*"),
		"routes/readme.txt": "ignored",
		"routes/c.yaml":     routeYAML("c", "/c"),
		"routes/old/x.yml":  "ignored: [",
	})
	snap, err := Loader{ConfigPath: dir + "/gateway.json", Getenv: envMap(nil)}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Routes) != 3 || snap.Route("b") == nil {
		t.Fatalf("the snapshot should hold routes a, b and c: %d", len(snap.Routes))
	}
}
