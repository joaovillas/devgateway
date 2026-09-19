package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Origin says where an effective value came from.
type Origin string

const (
	OriginDefault Origin = "default"
	OriginFile    Origin = "file"
	OriginEnv     Origin = "env"
)

// Source identifies where a value came from: the file or the environment
// variable.
type Source struct {
	Origin Origin `json:"origin"`
	// Name is the file path or the variable name; empty for a default.
	Name string `json:"name,omitempty"`
}

const (
	BackendMemory = "memory"
	BackendNDJSON = "ndjson"
	BackendSQLite = "sqlite"
)

// EnvConfigPath points at gateway.json; every other value has its own variable.
const EnvConfigPath = "GATEWAY_CONFIG"

// Settings is the effective configuration of the process.
type Settings struct {
	TrafficPort         int
	AdminPort           int
	Seed                *uint64 // nil: randomness is not reproducible
	HistoryBackend      string
	HistoryPath         string
	HistoryCapacity     int
	HistoryRecord       bool
	HistoryExpose       bool
	CaptureMaxBodyBytes int
	// LearningEnabled turns on learning mode; can be changed at runtime.
	LearningEnabled bool
	RoutesDir       string
	// Sources holds where each value came from, keyed as in settingFields.
	Sources map[string]Source
}

// settingField describes one process value: its key in gateway.json, its
// environment variable, its default and how to read each form.
type settingField struct {
	key      string
	env      string
	setDef   func(*Settings)
	fromFile func(*Settings, GatewayFile, string) bool // false: absent from the file
	fromEnv  func(*Settings, string) error
	value    func(Settings) any
}

var settingFields = []settingField{
	{
		key: "ports.traffic", env: "GATEWAY_TRAFFIC_PORT",
		setDef: func(s *Settings) { s.TrafficPort = 8080 },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.TrafficPort, g.Ports, func(p *PortsFile) *int { return p.Traffic })
		},
		fromEnv: func(s *Settings, v string) error { return parseInt(v, &s.TrafficPort) },
		value:   func(s Settings) any { return s.TrafficPort },
	},
	{
		key: "ports.admin", env: "GATEWAY_ADMIN_PORT",
		setDef: func(s *Settings) { s.AdminPort = 8081 },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.AdminPort, g.Ports, func(p *PortsFile) *int { return p.Admin })
		},
		fromEnv: func(s *Settings, v string) error { return parseInt(v, &s.AdminPort) },
		value:   func(s Settings) any { return s.AdminPort },
	},
	{
		key: "seed", env: "GATEWAY_SEED",
		setDef: func(s *Settings) { s.Seed = nil },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			if g.Seed == nil {
				return false
			}
			v := *g.Seed
			s.Seed = &v
			return true
		},
		fromEnv: func(s *Settings, v string) error {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return fmt.Errorf("seed must be a non-negative integer")
			}
			s.Seed = &n
			return nil
		},
		value: func(s Settings) any {
			if s.Seed == nil {
				return nil
			}
			return *s.Seed
		},
	},
	{
		key: "history.backend", env: "GATEWAY_HISTORY_BACKEND",
		setDef: func(s *Settings) { s.HistoryBackend = BackendMemory },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.HistoryBackend, g.History, func(h *HistoryFile) *string { return h.Backend })
		},
		fromEnv: func(s *Settings, v string) error { s.HistoryBackend = v; return nil },
		value:   func(s Settings) any { return s.HistoryBackend },
	},
	{
		// Without an explicit value, LoadSettings picks the path from the backend.
		key: "history.path", env: "GATEWAY_HISTORY_PATH",
		setDef: func(s *Settings) { s.HistoryPath = "" },
		fromFile: func(s *Settings, g GatewayFile, dir string) bool {
			ok := setIf(&s.HistoryPath, g.History, func(h *HistoryFile) *string { return h.Path })
			if ok {
				s.HistoryPath = relativeTo(dir, s.HistoryPath)
			}
			return ok
		},
		fromEnv: func(s *Settings, v string) error { s.HistoryPath = v; return nil },
		value:   func(s Settings) any { return s.HistoryPath },
	},
	{
		key: "history.capacity", env: "GATEWAY_HISTORY_CAPACITY",
		setDef: func(s *Settings) { s.HistoryCapacity = 1000 },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.HistoryCapacity, g.History, func(h *HistoryFile) *int { return h.Capacity })
		},
		fromEnv: func(s *Settings, v string) error { return parseInt(v, &s.HistoryCapacity) },
		value:   func(s Settings) any { return s.HistoryCapacity },
	},
	{
		key: "history.record", env: "GATEWAY_HISTORY_RECORD",
		setDef: func(s *Settings) { s.HistoryRecord = true },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.HistoryRecord, g.History, func(h *HistoryFile) *bool { return h.Record })
		},
		fromEnv: func(s *Settings, v string) error { return parseBool(v, &s.HistoryRecord) },
		value:   func(s Settings) any { return s.HistoryRecord },
	},
	{
		key: "history.expose", env: "GATEWAY_HISTORY_EXPOSE",
		setDef: func(s *Settings) { s.HistoryExpose = true },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.HistoryExpose, g.History, func(h *HistoryFile) *bool { return h.Expose })
		},
		fromEnv: func(s *Settings, v string) error { return parseBool(v, &s.HistoryExpose) },
		value:   func(s Settings) any { return s.HistoryExpose },
	},
	{
		key: "capture.maxBodyBytes", env: "GATEWAY_CAPTURE_MAX_BODY_BYTES",
		setDef: func(s *Settings) { s.CaptureMaxBodyBytes = 64 << 10 },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.CaptureMaxBodyBytes, g.Capture, func(c *CaptureFile) *int { return c.MaxBodyBytes })
		},
		fromEnv: func(s *Settings, v string) error { return parseInt(v, &s.CaptureMaxBodyBytes) },
		value:   func(s Settings) any { return s.CaptureMaxBodyBytes },
	},
	{
		key: "learning.enabled", env: "GATEWAY_LEARNING",
		setDef: func(s *Settings) { s.LearningEnabled = false },
		fromFile: func(s *Settings, g GatewayFile, _ string) bool {
			return setIf(&s.LearningEnabled, g.Learning, func(l *LearningFile) *bool { return l.Enabled })
		},
		fromEnv: func(s *Settings, v string) error { return parseBool(v, &s.LearningEnabled) },
		value:   func(s Settings) any { return s.LearningEnabled },
	},
	{
		key: "routesDir", env: "GATEWAY_ROUTES_DIR",
		setDef: func(s *Settings) { s.RoutesDir = "routes" },
		fromFile: func(s *Settings, g GatewayFile, dir string) bool {
			if g.RoutesDir == nil {
				return false
			}
			s.RoutesDir = relativeTo(dir, *g.RoutesDir)
			return true
		},
		fromEnv: func(s *Settings, v string) error { s.RoutesDir = v; return nil },
		value:   func(s Settings) any { return s.RoutesDir },
	},
}

func setIf[P any, T any](dst *T, parent *P, get func(*P) *T) bool {
	if parent == nil {
		return false
	}
	v := get(parent)
	if v == nil {
		return false
	}
	*dst = *v
	return true
}

func parseInt(v string, dst *int) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("must be a whole number")
	}
	*dst = n
	return nil
}

func parseBool(v string, dst *bool) error {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("must be true or false")
	}
	*dst = b
	return nil
}

// relativeTo resolves the paths in the file against the directory of
// gateway.json itself, so the result does not depend on where the process
// runs from.
func relativeTo(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// EffectiveValue is a process value together with where it came from.
type EffectiveValue struct {
	Key    string `json:"key"`
	Env    string `json:"env"`
	Value  any    `json:"value"`
	Source Source `json:"source"`
}

// Effective lists every value with its source, in settingFields order.
func (s Settings) Effective() []EffectiveValue {
	out := make([]EffectiveValue, len(settingFields))
	for i, f := range settingFields {
		out[i] = EffectiveValue{Key: f.key, Env: f.env, Value: f.value(s), Source: s.Sources[f.key]}
	}
	return out
}

// LoadSettings assembles the process configuration: defaults, then
// gateway.json, then the environment. A missing gateway.json yields a
// warning, not an error.
func LoadSettings(configPath string, getenv func(string) (string, bool)) (Settings, []string, error) {
	data, err := os.ReadFile(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return loadSettings(configPath, nil, false, getenv)
	case err != nil:
		return Settings{}, nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	return loadSettings(configPath, data, true, getenv)
}

// SettingsFrom assembles the process configuration like LoadSettings, but
// with data standing in for the content of configPath, without touching the
// disk. It serves whoever validates a gateway.json before writing it.
func SettingsFrom(configPath string, data []byte, getenv func(string) (string, bool)) (Settings, []string, error) {
	return loadSettings(configPath, data, true, getenv)
}

func loadSettings(configPath string, data []byte, exists bool, getenv func(string) (string, bool)) (Settings, []string, error) {
	var s Settings
	s.Sources = map[string]Source{}
	for _, f := range settingFields {
		f.setDef(&s)
		s.Sources[f.key] = Source{Origin: OriginDefault}
	}

	var warnings []string
	x := &nodeIndex{file: configPath}
	if !exists {
		warnings = append(warnings, fmt.Sprintf("%s not found; using default values", configPath))
	} else {
		var g GatewayFile
		var err error
		if err := jsonSyntax(configPath, data); err != nil {
			return s, nil, err
		}
		if x, err = decodeDocument(configPath, data, &g); err != nil {
			return s, nil, err
		}
		dir := filepath.Dir(configPath)
		for _, f := range settingFields {
			if f.fromFile(&s, g, dir) {
				s.Sources[f.key] = Source{Origin: OriginFile, Name: configPath}
			}
		}
	}

	var errs Errors
	for _, f := range settingFields {
		v, ok := getenv(f.env)
		if !ok || v == "" {
			continue
		}
		if err := f.fromEnv(&s, v); err != nil {
			errs = append(errs, envError(f, err.Error()))
			continue
		}
		s.Sources[f.key] = Source{Origin: OriginEnv, Name: f.env}
	}
	if len(errs) > 0 {
		return s, nil, errs
	}

	// Default paths sit next to gateway.json, like the ones in the file; only
	// the ones from the environment start at the working directory.
	dir := filepath.Dir(configPath)
	if s.Sources["routesDir"].Origin == OriginDefault {
		s.RoutesDir = relativeTo(dir, s.RoutesDir)
	}
	if s.HistoryPath == "" {
		switch s.HistoryBackend {
		case BackendNDJSON:
			s.HistoryPath = relativeTo(dir, "gateway-history.ndjson")
		case BackendSQLite:
			s.HistoryPath = relativeTo(dir, "gateway-history.db")
		}
	}
	if err := s.validate(x); err != nil {
		return s, nil, err
	}
	return s, warnings, nil
}

func envError(f settingField, msg string) *Error {
	return &Error{File: "environment variable " + f.env, Field: f.key, Msg: msg}
}

// validate rejects invalid values, naming the file or the variable each one
// came from.
func (s Settings) validate(x *nodeIndex) error {
	var errs Errors
	fail := func(key, format string, args ...any) {
		src := s.Sources[key]
		msg := fmt.Sprintf(format, args...)
		switch src.Origin {
		case OriginEnv:
			errs = append(errs, &Error{File: "environment variable " + src.Name, Field: key, Msg: msg})
		case OriginFile:
			errs = append(errs, x.locate(issue{field: key, msg: msg}))
		default:
			errs = append(errs, &Error{File: "built-in default", Field: key, Msg: msg})
		}
	}
	for _, p := range []struct {
		key  string
		port int
	}{{"ports.traffic", s.TrafficPort}, {"ports.admin", s.AdminPort}} {
		if p.port < 0 || p.port > 65535 {
			fail(p.key, "port must be between 0 and 65535, where 0 picks a free port (got %d)", p.port)
		}
	}
	if s.TrafficPort == s.AdminPort && s.TrafficPort != 0 {
		fail("ports.admin", "the admin port is the same as the traffic port (%d); the two have to differ", s.AdminPort)
	}
	switch s.HistoryBackend {
	case BackendMemory, BackendNDJSON, BackendSQLite:
	default:
		fail("history.backend", "unknown backend %q; use memory, ndjson or sqlite", s.HistoryBackend)
	}
	if s.HistoryCapacity < 1 {
		fail("history.capacity", "capacity must be at least 1 (got %d)", s.HistoryCapacity)
	}
	if s.CaptureMaxBodyBytes < 0 {
		fail("capture.maxBodyBytes", "cannot be negative (got %d)", s.CaptureMaxBodyBytes)
	}
	if s.RoutesDir == "" {
		fail("routesDir", "required")
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}
