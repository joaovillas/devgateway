// Package gateway expõe os artefatos embutidos no binário.
package gateway

import (
	"embed"
	"io/fs"
)

// O diretório web/dist sempre existe: um index.html mínimo é versionado para
// que go build funcione num clone limpo, sem ter rodado o build do frontend.
//
//go:embed all:web/dist
var webDist embed.FS

// WebFS devolve o frontend construído, com web/dist como raiz.
func WebFS() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
