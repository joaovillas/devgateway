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

// Error é um problema de configuração localizado: arquivo, campo e posição.
type Error struct {
	File   string
	Field  string
	Line   int
	Column int
	Msg    string
	// Conflict marca uma colisão com outro documento (nome ou casamento
	// repetido, arquivo já existente), distinta de um valor inválido.
	Conflict bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Field != "" {
		fmt.Fprintf(&b, ": campo %s", e.Field)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " (linha %d, coluna %d)", e.Line, e.Column)
	}
	b.WriteString(": ")
	b.WriteString(e.Msg)
	return b.String()
}

// Errors agrupa vários problemas encontrados numa mesma carga.
type Errors []*Error

// HasConflict informa se algum dos problemas é uma colisão entre documentos.
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

// issue é um problema ainda sem arquivo, identificado pelo caminho do campo.
type issue struct {
	field string
	msg   string
}

func issuef(field, format string, args ...any) issue {
	return issue{field: field, msg: fmt.Sprintf(format, args...)}
}

// nodeIndex associa cada caminho de campo ao nó YAML onde ele aparece.
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

// locate converte um issue num Error, com a posição do campo quando o campo
// existe no documento, ou a do ancestral mais próximo quando ele está ausente.
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

// fieldAtLine devolve o campo mais profundo cujo valor começa na linha dada.
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
	typeMatcher  = reflect.TypeFor[Matcher]()
	typeDuration = reflect.TypeFor[Duration]()
)

// checkShape percorre o documento junto com o tipo de destino, indexando cada
// campo e apontando chaves desconhecidas, que o decodificador ignoraria.
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
		t = reflect.TypeFor[latencyRange]()
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
				*issues = append(*issues, issuef(child, "campo desconhecido"))
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

// decodeDocument interpreta data como YAML (JSON incluído) no destino v,
// devolvendo o índice de nós para localizar erros posteriores.
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
	// A versão vem antes de tudo: um documento de schema futuro pode ter
	// campos que este binário desconhece, e o erro útil é o de versão.
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

// translateYAMLErr converte as mensagens "line N: ..." do decodificador em
// erros localizados, nomeando o campo quando a linha o identifica.
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
		return fmt.Sprintf("valor %q (%s) não é aceito como %s", g[2], g[1], g[3])
	}
	return m
}

// ParseGatewayFile interpreta o conteúdo de gateway.json.
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

// jsonSyntax recusa o que não é JSON, já que o decodificador YAML aceitaria
// comentários e outras construções fora do formato de gateway.json.
func jsonSyntax(file string, data []byte) error {
	var v any
	err := json.Unmarshal(data, &v)
	if err == nil {
		return nil
	}
	e := &Error{File: file, Msg: "JSON inválido: " + err.Error()}
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
	return issuef("schemaVersion", "versão de schema %d é maior que a suportada por este binário (%d); atualize o gateway", v, SchemaVersion)
}

// MarshalGatewayFile serializa gateway.json.
func MarshalGatewayFile(g GatewayFile) ([]byte, error) {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseRoute interpreta um documento de rota. Erros de estrutura e de valor
// saem localizados; a validação de regras fica em Validate.
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

// ParseRouteDoc interpreta um documento de rota guardando a posição de cada
// campo, para que os erros da validação posterior (BuildRoutes) apontem linha
// e coluna no texto dado.
func ParseRouteDoc(file string, data []byte) (RouteDoc, error) {
	r, x, err := parseRoute(file, data)
	if err != nil {
		return RouteDoc{}, err
	}
	return RouteDoc{File: file, Route: r, index: x}, nil
}

// DecodeJSON interpreta data, que precisa ser JSON, no destino v com as
// mesmas regras de forma dos documentos: campos desconhecidos são recusados
// e cada erro nomeia o campo e sua posição no texto. label identifica o
// texto nas mensagens.
func DecodeJSON(label string, data []byte, v any) error {
	if err := jsonSyntax(label, data); err != nil {
		return err
	}
	_, err := decodeDocument(label, data, v)
	return err
}

// HasComments informa se o documento YAML contém comentários, que a
// reescrita por MarshalRoute perderia. Um documento ilegível é tratado como
// sem comentários.
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

// MarshalRoute serializa um documento de rota em YAML.
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
