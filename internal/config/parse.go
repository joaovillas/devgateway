package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Error is a configuration problem pinned down to a file, a field and a
// position.
type Error struct {
	File   string
	Field  string
	Line   int
	Column int
	Msg    string
	// Conflict marks a collision with another document (duplicate name or
	// match, file already there), as opposed to an invalid value.
	Conflict bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Field != "" {
		fmt.Fprintf(&b, ": field %s", e.Field)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " (line %d, column %d)", e.Line, e.Column)
	}
	b.WriteString(": ")
	b.WriteString(e.Msg)
	return b.String()
}

// Errors groups the problems found in a single load.
type Errors []*Error

// HasConflict reports whether any of the problems is a collision between
// documents.
func (es Errors) HasConflict() bool {
	for _, e := range es {
		if e.Conflict {
			return true
		}
	}
	return false
}

func (es Errors) Error() string {
	msgs := make([]string, len(es))
	for i, e := range es {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

// issue is a problem that has no file yet, identified by its field path.
type issue struct {
	field string
	msg   string
}

func issuef(field, format string, args ...any) issue {
	return issue{field: field, msg: fmt.Sprintf(format, args...)}
}

// nodeIndex maps each field path to the YAML node where it appears.
type nodeIndex struct {
	file    string
	entries []indexEntry
}

type indexEntry struct {
	field string
	key   *yaml.Node
	value *yaml.Node
}

func (x *nodeIndex) add(field string, key, value *yaml.Node) {
	x.entries = append(x.entries, indexEntry{field, key, value})
}

// locate turns an issue into an Error, carrying the position of the field
// when the field is in the document, or that of the closest ancestor when it
// is missing.
func (x *nodeIndex) locate(is issue) *Error {
	e := &Error{File: x.file, Field: is.field, Msg: is.msg}
	for field := is.field; ; {
		for _, en := range x.entries {
			if en.field == field {
				n := en.value
				if en.key != nil {
					n = en.key
				}
				e.Line, e.Column = n.Line, n.Column
				return e
			}
		}
		if field == "" {
			return e
		}
		field = parentField(field)
	}
}

// fieldAtLine returns the deepest field whose value starts on the given line.
func (x *nodeIndex) fieldAtLine(line int) (indexEntry, bool) {
	var best indexEntry
	found := false
	for _, en := range x.entries {
		if en.value.Line == line && (!found || len(en.field) > len(best.field)) {
			best, found = en, true
		}
	}
	return best, found
}

func parentField(field string) string {
	i := strings.LastIndexAny(field, ".[")
	if i < 0 {
		return ""
	}
	return field[:i]
}

var (
	typeLatency  = reflect.TypeFor[Latency]()
	typeDrop     = reflect.TypeFor[Drop]()
	typeMatcher  = reflect.TypeFor[Matcher]()
	typeDuration = reflect.TypeFor[Duration]()
)

// checkShape walks the document alongside the destination type, indexing
// every field and flagging unknown keys, which the decoder would otherwise
// ignore.
func checkShape(n *yaml.Node, t reflect.Type, field string, x *nodeIndex, issues *[]issue) {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case typeDuration:
		return
	case typeLatency:
		t = reflect.TypeFor[latencyFields]()
	case typeDrop:
		t = reflect.TypeFor[dropFields]()
	case typeMatcher:
		t = reflect.TypeFor[matcherFields]()
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := joinField(field, k.Value)
			x.add(child, k, v)
			ft, ok := fields[k.Value]
			if !ok {
				*issues = append(*issues, issuef(child, "unknown field"))
				continue
			}
			checkShape(v, ft, child, x, issues)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range n.Content {
			child := fmt.Sprintf("%s[%d]", field, i)
			x.add(child, nil, item)
			checkShape(item, t.Elem(), child, x, issues)
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := joinField(field, k.Value)
			x.add(child, k, v)
			checkShape(v, t.Elem(), child, x, issues)
		}
	}
}

func joinField(parent, key string) string {
	if parent == "" {
		return key
	}
	if key == "" {
		return parent
	}
	return parent + "." + key
}

func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

var yamlLineErr = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// decodeDocument reads data as YAML (JSON included) into the destination v,
// returning the node index used to locate later errors.
func decodeDocument(file string, data []byte, v any) (*nodeIndex, error) {
	x := &nodeIndex{file: file}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, translateYAMLErr(x, err)
	}
	if len(root.Content) == 0 {
		return x, nil
	}
	doc := root.Content[0]
	x.add("", nil, doc)
	// The version comes before everything else: a document from a future
	// schema may carry fields this binary does not know, and the useful error
	// is the one about the version.
	if k, v := mappingValue(doc, "schemaVersion"); v != nil {
		if n, err := strconv.Atoi(v.Value); err == nil && n > SchemaVersion {
			x.add("schemaVersion", k, v)
			return nil, x.errors([]issue{schemaIssue(n)})
		}
	}
	var issues []issue
	checkShape(doc, reflect.TypeOf(v).Elem(), "", x, &issues)
	if len(issues) > 0 {
		return nil, x.errors(issues)
	}
	if err := doc.Decode(v); err != nil {
		return nil, translateYAMLErr(x, err)
	}
	return x, nil
}

func (x *nodeIndex) errors(issues []issue) Errors {
	es := make(Errors, len(issues))
	for i, is := range issues {
		es[i] = x.locate(is)
	}
	return es
}

// translateYAMLErr turns the decoder's "line N: ..." messages into located
// errors, naming the field when the line identifies one.
func translateYAMLErr(x *nodeIndex, err error) error {
	var msgs []string
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs = te.Errors
	} else {
		msgs = []string{err.Error()}
	}
	var es Errors
	for _, m := range msgs {
		e := &Error{File: x.file, Msg: m}
		if g := yamlLineErr.FindStringSubmatch(m); g != nil {
			line, _ := strconv.Atoi(g[1])
			e.Line, e.Msg = line, translateYAMLMsg(g[2])
			if en, ok := x.fieldAtLine(line); ok {
				e.Field, e.Column = en.field, en.value.Column
			}
		}
		es = append(es, e)
	}
	return es
}

var cannotUnmarshal = regexp.MustCompile("^cannot unmarshal !!(\\w+) `(.*)` into (.+)$")

func translateYAMLMsg(m string) string {
	if g := cannotUnmarshal.FindStringSubmatch(m); g != nil {
		return fmt.Sprintf("value %q (%s) is not accepted as %s", g[2], g[1], g[3])
	}
	return m
}

// ParseGatewayFile reads the content of gateway.json.
func ParseGatewayFile(file string, data []byte) (GatewayFile, error) {
	var g GatewayFile
	if err := jsonSyntax(file, data); err != nil {
		return g, err
	}
	_, err := decodeDocument(file, data, &g)
	return g, err
}

func mappingValue(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

// jsonSyntax rejects anything that is not JSON, since the YAML decoder would
// accept comments and other constructs that the gateway.json format does not
// allow.
func jsonSyntax(file string, data []byte) error {
	var v any
	err := json.Unmarshal(data, &v)
	if err == nil {
		return nil
	}
	e := &Error{File: file, Msg: "invalid JSON: " + err.Error()}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		e.Line, e.Column = lineCol(data, se.Offset)
	}
	return Errors{e}
}

func lineCol(data []byte, offset int64) (int, int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	before := data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(offset) - bytes.LastIndexByte(before, '\n')
	return line, col
}

func schemaIssue(v int) issue {
	return issuef("schemaVersion", "schema version %d is newer than this binary supports (%d); update the gateway", v, SchemaVersion)
}

// MarshalGatewayFile serializes gateway.json.
func MarshalGatewayFile(g GatewayFile) ([]byte, error) {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseRoute reads a route document. Shape and value errors come out located;
// rule validation lives in Validate.
func ParseRoute(file string, data []byte) (Route, error) {
	r, _, err := parseRoute(file, data)
	return r, err
}

func parseRoute(file string, data []byte) (Route, *nodeIndex, error) {
	var r Route
	x, err := decodeDocument(file, data, &r)
	if err != nil {
		return r, nil, err
	}
	return r, x, nil
}

// ParseRouteDoc reads a route document keeping the position of every field,
// so that the errors from the later validation (BuildRoutes) point at a line
// and a column in the given text.
func ParseRouteDoc(file string, data []byte) (RouteDoc, error) {
	r, x, err := parseRoute(file, data)
	if err != nil {
		return RouteDoc{}, err
	}
	return RouteDoc{File: file, Route: r, index: x}, nil
}

// DecodeJSON reads data, which has to be JSON, into the destination v with
// the same shape rules as the documents: unknown fields are rejected and each
// error names the field and its position in the text. label identifies the
// text in the messages.
func DecodeJSON(label string, data []byte, v any) error {
	if err := jsonSyntax(label, data); err != nil {
		return err
	}
	_, err := decodeDocument(label, data, v)
	return err
}

// HasComments reports whether the YAML document has comments, which rewriting
// it through MarshalRoute would lose. An unreadable document counts as having
// no comments.
func HasComments(data []byte) bool {
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil {
		return false
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
			return true
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(&root)
}

// MarshalRoute serializes a route document as YAML.
func MarshalRoute(r Route) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
