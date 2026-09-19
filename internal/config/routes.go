package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RouteDoc is a route document together with the file it came from.
type RouteDoc struct {
	File  string
	Route Route
	index *nodeIndex
}

// NewRouteDoc wraps a route that did not come from disk (from the API, for
// instance), so that validation errors name the file it will live in.
func NewRouteDoc(file string, r Route) RouteDoc {
	return RouteDoc{File: file, Route: r, index: &nodeIndex{file: file}}
}

// Locate builds a validation error for a field of the document, with the
// position of the field when the document came from text (ParseRouteDoc).
func (d RouteDoc) Locate(field, msg string) *Error {
	x := d.index
	if x == nil {
		x = &nodeIndex{file: d.File}
	}
	return x.locate(issue{field: field, msg: msg})
}

// IsRouteFile reports whether the name has a route document extension.
func IsRouteFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// ReadRoutesDir reads every route document in the directory, in name order.
// Files with an unrecognized extension and subdirectories are skipped. An
// empty or missing directory is not an error: it yields a warning and no
// routes.
func ReadRoutesDir(dir string) ([]RouteDoc, []string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, []string{fmt.Sprintf("routes directory %s does not exist; starting with no routes", dir)}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the routes directory: %w", err)
	}
	var docs []RouteDoc
	var errs Errors
	for _, e := range entries {
		if e.IsDir() || !IsRouteFile(e.Name()) {
			continue
		}
		file := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, &Error{File: file, Msg: err.Error()})
			continue
		}
		r, x, err := parseRoute(file, data)
		if err != nil {
			errs = appendErr(errs, file, err)
			continue
		}
		docs = append(docs, RouteDoc{File: file, Route: r, index: x})
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	var warnings []string
	if len(docs) == 0 {
		warnings = append(warnings, fmt.Sprintf("routes directory %s holds no .yaml documents; starting with no routes", dir))
	}
	return docs, warnings, nil
}

func appendErr(errs Errors, file string, err error) Errors {
	var es Errors
	if errors.As(err, &es) {
		return append(errs, es...)
	}
	return append(errs, &Error{File: file, Msg: err.Error()})
}

// BuildRoutes validates the documents, detects collisions between them and
// returns the compiled routes in precedence order.
func BuildRoutes(docs []RouteDoc) ([]*CompiledRoute, error) {
	var errs Errors
	var routes []*CompiledRoute
	for _, d := range docs {
		x := d.index
		if x == nil {
			x = &nodeIndex{file: d.File}
		}
		if is := validateRoute(d.Route); len(is) > 0 {
			errs = append(errs, x.errors(is)...)
			continue
		}
		c, is := compileRoute(d.File, d.Route)
		if len(is) > 0 {
			errs = append(errs, x.errors(is)...)
			continue
		}
		c.index = x
		routes = append(routes, c)
	}
	errs = append(errs, collisions(routes)...)
	if len(errs) > 0 {
		return nil, errs
	}
	sortRoutes(routes)
	return routes, nil
}

// collisions rejects duplicate names and identical matches across documents.
// Patterns that merely overlap are legitimate and are left to precedence.
func collisions(routes []*CompiledRoute) Errors {
	var errs Errors
	byName := map[string]*CompiledRoute{}
	byMatch := map[RouteMatch]*CompiledRoute{}
	locate := func(r *CompiledRoute, field, format string, args ...any) *Error {
		e := r.index.locate(issuef(field, format, args...))
		e.Conflict = true
		return e
	}
	for _, r := range routes {
		if prev, ok := byName[r.Doc.Name]; ok {
			errs = append(errs, locate(r, "name", "route %q is already declared in %s", r.Doc.Name, prev.File))
		} else {
			byName[r.Doc.Name] = r
		}
		key := RouteMatch{Host: strings.ToLower(r.Doc.Match.Host), Path: r.Doc.Match.Path}
		if prev, ok := byMatch[key]; ok {
			errs = append(errs, locate(r, "match", "match %s is identical to the one in %s (route %q)", r.Pattern(), prev.File, prev.Doc.Name))
		} else {
			byMatch[key] = r
		}
	}
	slices.SortFunc(errs, func(a, b *Error) int { return strings.Compare(a.File, b.File) })
	return errs
}
