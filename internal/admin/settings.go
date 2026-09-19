package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/config/writer"
	"github.com/gamerjp64/devgateway/internal/store"
)

func (h *Handler) settingsRoutes() {
	h.handle("/api/settings", map[string]http.HandlerFunc{
		"GET":   h.getSettings,
		"PATCH": h.patchSettings,
	})
	h.handle("/api/settings/document", map[string]http.HandlerFunc{
		"GET": h.getSettingsDocument,
		"PUT": h.putSettingsDocument,
	})
	h.handle("/api/learning", map[string]http.HandlerFunc{
		"GET": h.getLearning,
		"PUT": h.putLearning,
	})
	h.handle("/api/reload", map[string]http.HandlerFunc{
		"POST": h.reload,
	})
}

// settingValue is an effective value plus whether the environment locks it.
type settingValue struct {
	config.EffectiveValue
	// Locked is true when the value comes from an environment variable: the
	// API refuses to change it.
	Locked bool `json:"locked"`
}

type settingsFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type settingsView struct {
	File   settingsFile   `json:"file"`
	Values []settingValue `json:"values"`
}

func (h *Handler) settingsView() settingsView {
	s := h.live.Load().Settings
	eff := s.Effective()
	v := settingsView{
		File:   settingsFile{Path: h.loader.ConfigPath},
		Values: make([]settingValue, len(eff)),
	}
	if st, err := os.Stat(h.loader.ConfigPath); err == nil && !st.IsDir() {
		v.File.Exists = true
	}
	for i, e := range eff {
		v.Values[i] = settingValue{EffectiveValue: e, Locked: e.Source.Origin == config.OriginEnv}
	}
	return v
}

// getSettings returns the effective process configuration with the origin
// of each value: environment, file or default.
func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.settingsView())
}

// settingsResult is the response to a change of the process configuration.
type settingsResult struct {
	Settings settingsView `json:"settings"`
	// Applied are the keys whose effective value changed.
	Applied []string `json:"applied"`
	// Notes explain effects the user needs to know about: the old port that
	// stopped accepting connections, the history that was not migrated.
	Notes []string `json:"notes"`
}

// patchSettings changes gateway.json with a JSON Merge Patch over the file
// format and hot-applies the result. A touched key whose value comes from
// the environment rejects the whole change, leaving the file untouched.
func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request) {
	patch, err := readPatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.checkLocked(touchedKeys(patch, ""), nil); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.changeSettings(r.Header.Get("If-Match"), func(cur []byte) ([]byte, error) {
		return h.patchedFile(cur, patch)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, res.version)
	writeJSON(w, http.StatusOK, res.body)
}

// patchedFile applies the merge patch over the current contents of
// gateway.json (empty when the file does not exist) and returns the new
// file, serialized.
func (h *Handler) patchedFile(cur []byte, patch map[string]any) ([]byte, error) {
	var base any = map[string]any{}
	if len(bytes.TrimSpace(cur)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(cur))
		dec.UseNumber()
		if err := dec.Decode(&base); err != nil {
			// The file on disk became unreadable since it was loaded;
			// validating it points at the problem.
			_, perr := config.ParseGatewayFile(h.loader.ConfigPath, cur)
			if perr == nil {
				perr = err
			}
			return nil, perr
		}
	} else {
		// A gateway.json created by the API declares the schema version.
		base = map[string]any{"schemaVersion": json.Number(fmt.Sprint(config.SchemaVersion))}
	}
	merged, err := json.Marshal(mergePatch(base, patch))
	if err != nil {
		return nil, err
	}
	var g config.GatewayFile
	if err := config.DecodeJSON(h.loader.ConfigPath, merged, &g); err != nil {
		return nil, err
	}
	return config.MarshalGatewayFile(g)
}

// touchedKeys lists the process configuration keys the merge patch
// reaches: a leaf names its own key, and an object replaced by null, or by
// a value that is not an object, reaches every key below it.
func touchedKeys(patch map[string]any, prefix string) []string {
	var out []string
	for k, v := range patch {
		path := prefix + k
		if sub, ok := v.(map[string]any); ok {
			out = append(out, touchedKeys(sub, path+".")...)
			continue
		}
		for _, e := range (config.Settings{}).Effective() {
			if e.Key == path || strings.HasPrefix(e.Key, path+".") {
				out = append(out, e.Key)
			}
		}
	}
	slices.Sort(out)
	return out
}

// checkLocked refuses to change values that come from the environment,
// naming the variable responsible. keys are the keys the change reaches;
// allow, when present, waives the refusal for a key whose value does not
// actually change.
func (h *Handler) checkLocked(keys []string, allow func(config.EffectiveValue) bool) error {
	for _, e := range h.live.Load().Settings.Effective() {
		if e.Source.Origin != config.OriginEnv || !slices.Contains(keys, e.Key) {
			continue
		}
		if allow != nil && allow(e) {
			continue
		}
		return &requestError{http.StatusConflict, apiError{
			Error: "locked",
			Field: e.Key,
			Env:   e.Source.Name,
			Message: fmt.Sprintf("%s comes from environment variable %s and cannot be changed through the API; %s was left untouched",
				e.Key, e.Source.Name, filepath.Base(h.loader.ConfigPath)),
		}}
	}
	return nil
}

// settingsChange is an applied change to gateway.json.
type settingsChange struct {
	body    settingsResult
	version string
}

// changeSettings writes into gateway.json the contents edit produces from
// the current ones and hot-applies the resulting configuration, in this
// order: validate the new file; prepare the changed ports and history
// backend; write the file atomically; swap ports, backend and snapshot.
// Any failure keeps the configuration in force and the file intact. It all
// happens under the write mutex, so that two concurrent changes do not
// lose each other.
func (h *Handler) changeSettings(ifMatch string, edit func(cur []byte) ([]byte, error)) (settingsChange, error) {
	path := h.loader.ConfigPath
	var data []byte
	unchanged := false
	var beforeTraffic, beforeAdmin int
	change, snap, switched, err := h.replace(writer.CauseAPI, func() (*config.Snapshot, error) {
		cur, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if ifMatch != "" && (!exists || writer.Version(cur) != ifMatch) {
			return nil, writer.ErrStale
		}
		if data, err = edit(cur); err != nil {
			return nil, err
		}
		unchanged = exists && bytes.Equal(cur, data)
		return h.settingsSnapshot(data)
	}, func() error {
		if unchanged {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating the directory of %s: %w", path, err)
		}
		if err := writer.WriteFileAtomic(path, data); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		return nil
	}, func() {
		beforeTraffic, beforeAdmin = h.currentPorts()
	})
	if err != nil {
		return settingsChange{}, err
	}
	res := settingsResult{Settings: h.settingsView(), Applied: change.Settings, Notes: []string{}}
	if res.Applied == nil {
		res.Applied = []string{}
	}
	traffic, adminPort := h.currentPorts()
	if traffic != beforeTraffic {
		res.Notes = append(res.Notes, fmt.Sprintf("the traffic port is now %d; %d stopped accepting connections and is finishing the requests in flight", traffic, beforeTraffic))
	}
	if adminPort != beforeAdmin {
		res.Notes = append(res.Notes, fmt.Sprintf("the admin port is now %d; %d stops accepting connections after this response", adminPort, beforeAdmin))
	}
	if switched != "" {
		s := snap.Settings
		where := store.BackendName(s)
		if where != config.BackendMemory {
			where += " (" + s.HistoryPath + ")"
		}
		res.Notes = append(res.Notes, fmt.Sprintf("the history is now in %s; the earlier exchanges stay in the %s backend and were not migrated", where, switched))
	}
	return settingsChange{body: res, version: writer.Version(data)}, nil
}

// settingsSnapshot builds the snapshot with gateway.json holding data. The
// routes in force are kept; only a different routes directory is read from
// disk.
func (h *Handler) settingsSnapshot(data []byte) (*config.Snapshot, error) {
	cur := h.live.Load()
	s, _, err := config.SettingsFrom(h.loader.ConfigPath, data, h.loader.Getenv)
	if err != nil {
		return nil, err
	}
	if s.RoutesDir == cur.Settings.RoutesDir {
		return config.NewSnapshot(s, cur.Routes, cur.Warnings), nil
	}
	docs, warnings, err := config.ReadRoutesDir(s.RoutesDir)
	if err != nil {
		return nil, err
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		return nil, err
	}
	return config.NewSnapshot(s, routes, warnings), nil
}

func (h *Handler) currentPorts() (int, int) {
	if h.ports != nil {
		return h.ports()
	}
	s := h.live.Load().Settings
	return s.TrafficPort, s.AdminPort
}

// replace swaps the whole configuration for the snapshot load builds,
// hot-applying what lives outside it (ports, history backend) before
// publishing it. persist, when present, writes what has to be written once
// everything is prepared and before the swap. before runs under the write
// mutex, ahead of any swap. Besides the change, it returns the name of the
// previous history backend when that backend was switched, and publishes
// the matching history event.
func (h *Handler) replace(cause string, load func() (*config.Snapshot, error), persist func() error, before func()) (writer.Change, *config.Snapshot, string, error) {
	var switched string
	change, snap, err := h.writer.Replace(cause, func() (*config.Snapshot, error) {
		if before != nil {
			before()
		}
		return load()
	}, func(old, next *config.Snapshot) error {
		prev := h.history.Backend()
		var err error
		if h.apply != nil {
			err = h.apply(old.Settings, next.Settings, persist)
		} else if persist != nil {
			err = persist()
		}
		if err == nil && store.Reopens(old.Settings, next.Settings) {
			switched = prev
		}
		return err
	})
	if err != nil {
		return change, nil, "", err
	}
	if switched != "" {
		h.events.publish("history", historyEvent{Cause: "backend", Backend: h.history.Backend()})
	}
	return change, snap, switched, nil
}

// getSettingsDocument returns gateway.json as it is on disk. Without the
// file, it returns {} and the X-Gateway-File-Exists: false header.
func (h *Handler) getSettingsDocument(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(h.loader.ConfigPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Gateway-File-Exists", "false")
		w.Write([]byte("{}\n"))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "reading "+h.loader.ConfigPath+": "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Gateway-File-Exists", "true")
	setETag(w, writer.Version(data))
	w.Write(data)
}

// putSettingsDocument writes gateway.json as sent and hot-applies it, with
// the same rules as PATCH /api/settings. A document that changes a value
// coming from the environment is rejected; one that only keeps what the
// file already declared, or repeats the value in force, is accepted.
func (h *Handler) putSettingsDocument(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r, []string{"application/json"})
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := config.ParseGatewayFile(h.loader.ConfigPath, data); err != nil {
		writeErr(w, err)
		return
	}
	cur, _ := os.ReadFile(h.loader.ConfigPath)
	oldDoc, newDoc := jsonTree(cur), jsonTree(data)
	var keys []string
	for _, e := range h.live.Load().Settings.Effective() {
		if !jsonEqual(valueAt(oldDoc, e.Key), valueAt(newDoc, e.Key)) {
			keys = append(keys, e.Key)
		}
	}
	err = h.checkLocked(keys, func(e config.EffectiveValue) bool {
		v := valueAt(newDoc, e.Key)
		return v != nil && jsonEqual(v, e.Value)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.changeSettings(r.Header.Get("If-Match"), func([]byte) ([]byte, error) { return data, nil })
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, res.version)
	writeJSON(w, http.StatusOK, res.body)
}

// jsonTree reads a JSON document as a generic tree; anything that is not
// JSON becomes nil.
func jsonTree(data []byte) any {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	return v
}

// valueAt returns the value at the dotted key ("history.backend"), or nil.
func valueAt(tree any, key string) any {
	for part := range strings.SplitSeq(key, ".") {
		m, ok := tree.(map[string]any)
		if !ok {
			return nil
		}
		tree = m[part]
	}
	return tree
}

// jsonEqual compares two values by their JSON form, so that 9090 (int) and
// 9090 (float64) come out equal.
func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var va, vb any
	json.Unmarshal(ja, &va)
	json.Unmarshal(jb, &vb)
	return reflect.DeepEqual(va, vb)
}

// learningView is the state of learning mode.
type learningView struct {
	Enabled bool          `json:"enabled"`
	Source  config.Source `json:"source"`
	Locked  bool          `json:"locked"`
	// Learned counts, per route, the overrides learning has written.
	Learned map[string]int `json:"learned"`
}

func (h *Handler) learningView() learningView {
	snap := h.live.Load()
	src := snap.Settings.Sources["learning.enabled"]
	v := learningView{
		Enabled: snap.Settings.LearningEnabled,
		Source:  src,
		Locked:  src.Origin == config.OriginEnv,
		Learned: map[string]int{},
	}
	for _, r := range snap.Routes {
		n := 0
		for _, o := range r.Doc.Overrides {
			if o.Source != nil && o.Source.Kind == config.SourceLearned {
				n++
			}
		}
		v.Learned[r.Name()] = n
	}
	return v
}

func (h *Handler) getLearning(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.learningView())
}

// putLearning turns learning mode on or off, writing learning.enabled to
// gateway.json.
func (h *Handler) putLearning(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{
			Error: "invalid", Field: "enabled", Message: "enabled is required: true turns learning on, false turns it off",
		})
		return
	}
	patch := map[string]any{"learning": map[string]any{"enabled": *body.Enabled}}
	if err := h.checkLocked([]string{"learning.enabled"}, nil); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := h.changeSettings(r.Header.Get("If-Match"), func(cur []byte) ([]byte, error) {
		return h.patchedFile(cur, patch)
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.learningView())
}

type reloadChanged struct {
	Routes   []string `json:"routes"`
	Settings []string `json:"settings"`
}

type reloadResult struct {
	Routes   int           `json:"routes"`
	Changed  reloadChanged `json:"changed"`
	Warnings []string      `json:"warnings"`
}

// reload re-reads gateway.json and the routes directory and hot-applies
// the result, ports and history backend included. A configuration that is
// invalid, or that cannot be applied, is rejected and the previous one
// stays in force; requests in flight finish under the snapshot they
// started with.
func (h *Handler) reload(w http.ResponseWriter, _ *http.Request) {
	change, snap, _, err := h.replace(writer.CauseReload, h.loader.Load, nil, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	warnings := snap.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, reloadResult{
		Routes:   len(snap.Routes),
		Changed:  reloadChanged{Routes: change.Routes, Settings: change.Settings},
		Warnings: warnings,
	})
}
