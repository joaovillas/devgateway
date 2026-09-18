package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/gamerjp64/gateway/internal/config"
)

// maxBody limita o corpo de uma escrita pela API.
const maxBody = 8 << 20

// bodyLabel nomeia o corpo da requisição nos erros de forma do JSON.
const bodyLabel = "corpo da requisição"

var (
	jsonTypes = []string{"application/json", "application/merge-patch+json"}
	yamlTypes = []string{"application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml", "text/plain"}
)

// readBody lê o corpo, recusando com 415 um Content-Type fora dos aceitos.
// Um corpo sem Content-Type é aceito como o formato esperado.
func readBody(r *http.Request, accepted []string) ([]byte, error) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || !slices.Contains(accepted, mt) {
			return nil, &requestError{http.StatusUnsupportedMediaType, apiError{
				Error:   "unsupported_media_type",
				Message: fmt.Sprintf("Content-Type %q não aceito aqui; use %s", ct, accepted[0]),
			}}
		}
	}
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, badRequest(fmt.Sprintf("corpo maior que o limite de %d bytes", maxBody))
		}
		return nil, badRequest("falha ao ler o corpo: " + err.Error())
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, badRequest("corpo ausente")
	}
	return b, nil
}

// decodeJSON lê o corpo JSON no destino v, com as regras de forma dos
// documentos: um campo desconhecido ou de tipo errado responde 422 nomeando
// o campo. JSON malformado responde 400.
func decodeJSON(r *http.Request, v any) error {
	b, err := readBody(r, jsonTypes)
	if err != nil {
		return err
	}
	return decodeJSONBytes(b, v)
}

func decodeJSONBytes(b []byte, v any) error {
	if !json.Valid(b) {
		return malformed(config.DecodeJSON(bodyLabel, b, new(any)))
	}
	if err := config.DecodeJSON(bodyLabel, b, v); err != nil {
		return err
	}
	return nil
}

// malformed converte o erro de sintaxe JSON, já localizado, num 400.
func malformed(err error) error {
	body := apiError{Error: "bad_request", Message: "JSON malformado"}
	var es config.Errors
	if errors.As(err, &es) && len(es) > 0 {
		body.Message = es[0].Error()
		body.File, body.Line, body.Column = es[0].File, es[0].Line, es[0].Column
	}
	return &requestError{http.StatusBadRequest, body}
}

// readPatch lê um JSON Merge Patch (RFC 7396), que precisa ser um objeto.
func readPatch(r *http.Request) (map[string]any, error) {
	b, err := readBody(r, jsonTypes)
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, malformed(config.DecodeJSON(bodyLabel, b, new(any)))
	}
	var p map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil || p == nil {
		return nil, badRequest("o merge patch precisa ser um objeto JSON")
	}
	return p, nil
}

// applyPatch aplica o merge patch sobre cur e decodifica o resultado em out,
// com as mesmas regras de forma de uma escrita completa.
func applyPatch(cur any, patch map[string]any, out any) error {
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	var target any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&target); err != nil {
		return err
	}
	merged, err := json.Marshal(mergePatch(target, patch))
	if err != nil {
		return err
	}
	return config.DecodeJSON(bodyLabel, merged, out)
}

// mergePatch aplica patch sobre target segundo a RFC 7396: chaves presentes
// substituem, null remove, objetos se fundem recursivamente.
func mergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
		} else {
			t[k] = mergePatch(t[k], v)
		}
	}
	return t
}
