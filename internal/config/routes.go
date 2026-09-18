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

// RouteDoc é um documento de rota com o arquivo de onde veio.
type RouteDoc struct {
	File  string
	Route Route
	index *nodeIndex
}

// NewRouteDoc embrulha uma rota que não veio do disco (por exemplo, da API),
// para que os erros de validação nomeiem o arquivo que ela ocupará.
func NewRouteDoc(file string, r Route) RouteDoc {
	return RouteDoc{File: file, Route: r, index: &nodeIndex{file: file}}
}

// IsRouteFile informa se o nome tem extensão de documento de rota.
func IsRouteFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// ReadRoutesDir lê todos os documentos de rota do diretório, em ordem de nome.
// Arquivos sem extensão reconhecida e subdiretórios são ignorados. Um
// diretório vazio ou ausente não é erro: devolve um aviso e nenhuma rota.
func ReadRoutesDir(dir string) ([]RouteDoc, []string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, []string{fmt.Sprintf("diretório de rotas %s não existe; iniciando sem rotas", dir)}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lendo diretório de rotas: %w", err)
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
		warnings = append(warnings, fmt.Sprintf("diretório de rotas %s não contém documentos .yaml; iniciando sem rotas", dir))
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

// BuildRoutes valida os documentos, detecta colisões entre eles e devolve as
// rotas compiladas em ordem de precedência.
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

// collisions recusa nomes repetidos e casamentos idênticos entre documentos.
// Padrões que apenas se sobrepõem são legítimos e ficam para a precedência.
func collisions(routes []*CompiledRoute) Errors {
	var errs Errors
	byName := map[string]*CompiledRoute{}
	byMatch := map[RouteMatch]*CompiledRoute{}
	locate := func(r *CompiledRoute, field, format string, args ...any) *Error {
		return r.index.locate(issuef(field, format, args...))
	}
	for _, r := range routes {
		if prev, ok := byName[r.Doc.Name]; ok {
			errs = append(errs, locate(r, "name", "rota %q já declarada em %s", r.Doc.Name, prev.File))
		} else {
			byName[r.Doc.Name] = r
		}
		key := RouteMatch{Host: strings.ToLower(r.Doc.Match.Host), Path: r.Doc.Match.Path}
		if prev, ok := byMatch[key]; ok {
			errs = append(errs, locate(r, "match", "casamento %s idêntico ao de %s (rota %q)", r.Pattern(), prev.File, prev.Doc.Name))
		} else {
			byMatch[key] = r
		}
	}
	slices.SortFunc(errs, func(a, b *Error) int { return strings.Compare(a.File, b.File) })
	return errs
}
