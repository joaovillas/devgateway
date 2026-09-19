// Package writer writes route documents to disk and applies the result
// without a restart. Every write — from the admin API or from learning mode —
// goes through a single Writer: under a write mutex, it re-reads the
// document, applies the change, validates the whole set of routes, writes the
// document atomically (temporary file plus rename) and swaps the snapshot in
// force. The other documents are left untouched.
package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"github.com/gamerjp64/devgateway/internal/config"
)

var (
	// ErrNotFound reports that the route is not in the configuration in force.
	ErrNotFound = errors.New("route not found")
	// ErrStale reports that the document on disk is not at the expected version.
	ErrStale = errors.New("the document changed since the version given")
)

// Causes of an applied change, as the event stream names them.
const (
	CauseAPI      = "api"
	CauseReload   = "reload"
	CauseLearning = "learning"
)

// Change describes an applied change.
type Change struct {
	Cause string
	// Routes are the routes created, changed or removed, by name.
	Routes []string
	// Settings are the process configuration keys that changed.
	Settings []string
}

// Result describes the document after a write.
type Result struct {
	// Changed reports whether the document was written (or removed).
	Changed bool
	// Created reports that the document did not exist and was created.
	Created bool
	// Snapshot is the snapshot published by the write, or the one in force
	// when nothing changed.
	Snapshot *config.Snapshot
	// Route is the name of the route after the write, which differs from the
	// requested one on a rename.
	Route string
	// File is the route document.
	File string
	// Version is the version of the document after the write (see Version);
	// empty on a removal.
	Version string
}

// Writer serializes configuration writes.
type Writer struct {
	live *config.Live

	mu sync.Mutex
	// files keeps document reads made outside the write mutex (by the API)
	// from overlapping with the replacement of the file: on Windows a file
	// open for reading cannot be replaced or removed.
	files sync.RWMutex

	hooksMu sync.Mutex
	hooks   []func(Change)
}

// New writes over the configuration in force in live.
func New(live *config.Live) *Writer { return &Writer{live: live} }

// OnChange registers fn to be called after each applied change, outside the
// write mutex.
func (w *Writer) OnChange(fn func(Change)) {
	w.hooksMu.Lock()
	w.hooks = append(w.hooks, fn)
	w.hooksMu.Unlock()
}

func (w *Writer) notify(c Change) {
	w.hooksMu.Lock()
	hooks := slices.Clone(w.hooks)
	w.hooksMu.Unlock()
	for _, fn := range hooks {
		fn(c)
	}
}

// Exclusive runs fn under the write mutex, for operations that have to
// exclude document writes, such as reloading from disk.
func (w *Writer) Exclusive(fn func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fn()
}

// ReadDocument reads a route document without racing the file replacement
// done by a write in progress.
func (w *Writer) ReadDocument(file string) ([]byte, error) {
	w.files.RLock()
	defer w.files.RUnlock()
	return os.ReadFile(file)
}

// RouteUpdate is a change to a route document.
type RouteUpdate struct {
	// Route is the name of the route in the configuration in force.
	Route string
	// IfMatch, when given, requires the document on disk to be at that
	// version (see Version); otherwise the write fails with ErrStale.
	IfMatch string
	// Cause is the cause reported to observers (CauseAPI, CauseLearning).
	Cause string
	// Apply changes the document read from disk and reports whether anything
	// changed. With no change, nothing is written. Apply may change the route
	// name, which renames it; the document stays in the same file.
	Apply func(*config.Route) (bool, error)
}

// UpdateRoute applies u to the route document and reports whether it was
// written. See Update.
func (w *Writer) UpdateRoute(u RouteUpdate) (bool, error) {
	res, err := w.Update(u)
	return res.Changed, err
}

// Update applies u to the route document. The document is re-read from disk
// under the mutex, so that the change starts from the version on disk and
// does not wipe out what another write has just stored. The resulting
// configuration is validated as a whole before anything is written: if it is
// invalid, nothing is written and the configuration in force stays put.
func (w *Writer) Update(u RouteUpdate) (Result, error) {
	w.mu.Lock()
	res, err := w.update(u)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: u.Cause, Routes: names(u.Route, res.Route)})
	}
	return res, err
}

func (w *Writer) update(u RouteUpdate) (Result, error) {
	snap := w.live.Load()
	cur, data, err := current(snap, u.Route, u.IfMatch)
	if err != nil {
		return Result{}, err
	}
	doc, err := config.ParseRoute(cur.File, data)
	if err != nil {
		return Result{}, err
	}
	changed, err := u.Apply(&doc)
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{Snapshot: snap, Route: u.Route, File: cur.File, Version: Version(data)}, nil
	}
	out, err := config.MarshalRoute(doc)
	if err != nil {
		return Result{}, err
	}
	rd := config.NewRouteDoc(cur.File, doc)
	next, err := w.commit(snap, cur, &rd, out)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: doc.Name, File: cur.File, Version: Version(out)}, nil
}

// current returns the route in force and the text of its document on disk,
// checking the expected version.
func current(snap *config.Snapshot, route, ifMatch string) (*config.CompiledRoute, []byte, error) {
	cur := snap.Route(route)
	if cur == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, route)
	}
	data, err := os.ReadFile(cur.File)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the document of route %s: %w", route, err)
	}
	if ifMatch != "" && Version(data) != ifMatch {
		return nil, nil, ErrStale
	}
	return cur, data, nil
}

// commit validates the set of routes with doc in place of replace (added,
// when replace is nil; dropped, when doc is nil), writes out to doc's file
// (or removes replace's) and publishes the new snapshot. Nothing is written
// if validation fails.
func (w *Writer) commit(snap *config.Snapshot, replace *config.CompiledRoute, doc *config.RouteDoc, out []byte) (*config.Snapshot, error) {
	docs := make([]config.RouteDoc, 0, len(snap.Routes)+1)
	for _, r := range snap.Routes {
		if r == replace {
			if doc != nil {
				docs = append(docs, *doc)
			}
			continue
		}
		docs = append(docs, config.NewRouteDoc(r.File, r.Doc))
	}
	if replace == nil && doc != nil {
		docs = append(docs, *doc)
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		return nil, err
	}
	w.files.Lock()
	defer w.files.Unlock()
	if doc == nil {
		if err := remove(replace.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("removing the document of route %s: %w", replace.Name(), err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(doc.File), 0o755); err != nil {
			return nil, fmt.Errorf("creating the routes directory: %w", err)
		}
		if err := WriteFileAtomic(doc.File, out); err != nil {
			return nil, fmt.Errorf("writing the document of route %s: %w", doc.Route.Name, err)
		}
	}
	next := config.NewSnapshot(snap.Settings, routes, snap.Warnings)
	w.live.Swap(next)
	return next, nil
}

// CreateRoute creates route r in a new document, {name}.yaml in the routes
// directory in force. A name already used by another route is rejected as a
// conflict. If {name}.yaml is the document of another route (which kept it
// through a rename), the route goes to the first free {name}-N.yaml; if the
// file exists but belongs to no route at all, the creation is rejected too.
func (w *Writer) CreateRoute(r config.Route, cause string) (Result, error) {
	w.mu.Lock()
	res, err := w.create(r.Name, func(file string) (config.RouteDoc, []byte, error) {
		out, err := config.MarshalRoute(r)
		if err != nil {
			return config.RouteDoc{}, nil, err
		}
		return config.NewRouteDoc(file, r), out, nil
	})
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: cause, Routes: []string{res.Route}})
	}
	return res, err
}

// fileOwner is the route in force whose document is the given file, or nil.
func fileOwner(snap *config.Snapshot, st os.FileInfo) *config.CompiledRoute {
	for _, r := range snap.Routes {
		if rs, err := os.Stat(r.File); err == nil && os.SameFile(rs, st) {
			return r
		}
	}
	return nil
}

// freeFile is the first {name}-N.yaml, starting at N = 2, that does not yet
// exist in the routes directory. An existing file is never overwritten,
// whether or not it is the document of a route.
func freeFile(dir, name string) string {
	for n := 2; ; n++ {
		file := filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", name, n))
		if _, err := os.Stat(file); err != nil {
			return file
		}
	}
}

func (w *Writer) create(name string, build func(file string) (config.RouteDoc, []byte, error)) (Result, error) {
	snap := w.live.Load()
	file := filepath.Join(snap.Settings.RoutesDir, name+".yaml")
	if cur := snap.Route(name); cur != nil {
		return Result{}, config.Errors{{File: cur.File, Field: "name", Conflict: true,
			Msg: fmt.Sprintf("route %q is already declared in %s", name, cur.File)}}
	}
	doc, out, err := build(file)
	if err != nil {
		return Result{}, err
	}
	if doc.Route.Name != name {
		return Result{}, config.Errors{doc.Locate("name",
			fmt.Sprintf("the declared name %q differs from the route name %q", doc.Route.Name, name))}
	}
	// Validation comes before the name is used as a file path.
	if _, err := config.BuildRoutes([]config.RouteDoc{doc}); err != nil {
		return Result{}, err
	}
	if st, err := os.Stat(file); err == nil {
		if fileOwner(snap, st) == nil {
			return Result{}, config.Errors{{File: file, Field: "name", Conflict: true,
				Msg: fmt.Sprintf("file %s already exists and matches no route in force; reload the configuration or pick another name", file)}}
		}
		// The file is the document of another route, which kept it through a
		// rename: the new route goes to the first free {name}-N.yaml.
		file = freeFile(snap.Settings.RoutesDir, name)
		if doc, out, err = build(file); err != nil {
			return Result{}, err
		}
	}
	next, err := w.commit(snap, nil, &doc, out)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Created: true, Snapshot: next, Route: name, File: file, Version: Version(out)}, nil
}

// DocumentWrite writes the text of a route document exactly as sent,
// comments included.
type DocumentWrite struct {
	// Route is the name of the route. The document has to declare that same
	// name. If the route does not exist, it is created.
	Route string
	Data  []byte
	// IfMatch, when given, requires the document on disk to be at that
	// version; a route that does not exist matches no version at all.
	IfMatch string
	Cause   string
}

// WriteDocument validates the text as it would on a load from disk, with
// errors pointing at a line and a column, and writes it without
// re-serializing.
func (w *Writer) WriteDocument(d DocumentWrite) (Result, error) {
	w.mu.Lock()
	res, err := w.writeDocument(d)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: d.Cause, Routes: []string{d.Route}})
	}
	return res, err
}

func (w *Writer) writeDocument(d DocumentWrite) (Result, error) {
	snap := w.live.Load()
	if snap.Route(d.Route) == nil {
		if d.IfMatch != "" {
			return Result{}, ErrStale
		}
		return w.create(d.Route, func(file string) (config.RouteDoc, []byte, error) {
			doc, err := config.ParseRouteDoc(file, d.Data)
			return doc, d.Data, err
		})
	}
	cur, data, err := current(snap, d.Route, d.IfMatch)
	if err != nil {
		return Result{}, err
	}
	doc, err := config.ParseRouteDoc(cur.File, d.Data)
	if err != nil {
		return Result{}, err
	}
	if doc.Route.Name != d.Route {
		return Result{}, config.Errors{doc.Locate("name",
			fmt.Sprintf("the declared name %q differs from the route name %q", doc.Route.Name, d.Route))}
	}
	if bytes.Equal(data, d.Data) {
		return Result{Snapshot: snap, Route: d.Route, File: cur.File, Version: Version(data)}, nil
	}
	next, err := w.commit(snap, cur, &doc, d.Data)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: d.Route, File: cur.File, Version: Version(d.Data)}, nil
}

// DeleteRoute drops the route and deletes its document.
func (w *Writer) DeleteRoute(route, ifMatch, cause string) (Result, error) {
	w.mu.Lock()
	res, err := w.deleteRoute(route, ifMatch)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: cause, Routes: []string{route}})
	}
	return res, err
}

func (w *Writer) deleteRoute(route, ifMatch string) (Result, error) {
	snap := w.live.Load()
	cur, _, err := current(snap, route, ifMatch)
	if err != nil {
		return Result{}, err
	}
	next, err := w.commit(snap, cur, nil, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: route, File: cur.File}, nil
}

// Reload builds a new configuration with load and, if it is valid, calls
// apply with the previous and the new one, to apply whatever lives outside
// the snapshot (ports, history backend). Only if apply succeeds is the new
// snapshot published; any failure preserves the configuration in force.
// Requests already in flight carry on with the snapshot they captured. It
// runs under the write mutex, so it does not interleave with a document write.
func (w *Writer) Reload(load func() (*config.Snapshot, error), apply func(old, next *config.Snapshot) error) (Change, *config.Snapshot, error) {
	return w.Replace(CauseReload, load, apply)
}

// Replace is Reload with the given cause, for whoever swaps the whole
// configuration for another reason, such as a change to gateway.json through
// the API. load runs under the write mutex, so it can read and change files
// without interleaving with another write. Outside a reload, a swap that
// changed nothing is not reported to observers.
func (w *Writer) Replace(cause string, load func() (*config.Snapshot, error), apply func(old, next *config.Snapshot) error) (Change, *config.Snapshot, error) {
	w.mu.Lock()
	old := w.live.Load()
	next, err := load()
	if err == nil && apply != nil {
		err = apply(old, next)
	}
	if err != nil {
		w.mu.Unlock()
		return Change{}, nil, err
	}
	w.live.Swap(next)
	w.mu.Unlock()
	c := Change{Cause: cause, Routes: diffRoutes(old, next), Settings: DiffSettings(old.Settings, next.Settings)}
	if cause == CauseReload || len(c.Routes) > 0 || len(c.Settings) > 0 {
		w.notify(c)
	}
	return c, next, nil
}

// diffRoutes names the routes created, removed or changed between the two
// snapshots, in alphabetical order.
func diffRoutes(old, next *config.Snapshot) []string {
	out := []string{}
	for _, r := range next.Routes {
		prev := old.Route(r.Name())
		if prev == nil || prev.File != r.File || !reflect.DeepEqual(prev.Doc, r.Doc) {
			out = append(out, r.Name())
		}
	}
	for _, r := range old.Routes {
		if next.Route(r.Name()) == nil {
			out = append(out, r.Name())
		}
	}
	slices.Sort(out)
	return out
}

// DiffSettings names the process configuration keys whose value or source
// changed, in Settings.Effective order.
func DiffSettings(old, next config.Settings) []string {
	out := []string{}
	a, b := old.Effective(), next.Effective()
	for i := range a {
		if !reflect.DeepEqual(a[i].Value, b[i].Value) || a[i].Source != b[i].Source {
			out = append(out, a[i].Key)
		}
	}
	return out
}

func names(before, after string) []string {
	if after == "" || after == before {
		return []string{before}
	}
	return []string{before, after}
}

// Version is the opaque version of a document, used as an ETag.
func Version(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, renamed over the destination: a reader sees either the previous
// content or the new one, never a half-finished write. The permissions of an
// existing file are preserved.
func WriteFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
