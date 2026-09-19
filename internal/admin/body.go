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

	"github.com/joaovillas/devgateway/internal/config"
)

// maxBody caps the body of a write through the API.
const maxBody = 8 << 20

// bodyLabel names the request body in the JSON shape errors.
const bodyLabel = "request body"

var (
	jsonTypes = []string{"application/json", "application/merge-patch+json"}
	yamlTypes = []string{"application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml", "text/plain"}
)

// readBody reads the body, rejecting with 415 a Content-Type outside the
// accepted ones. A body with no Content-Type is taken as the expected
// format.
func readBody(r *http.Request, accepted []string) ([]byte, error) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || !slices.Contains(accepted, mt) {
			return nil, &requestError{http.StatusUnsupportedMediaType, apiError{
				Error:   "unsupported_media_type",
				Message: fmt.Sprintf("Content-Type %q is not accepted here; use %s", ct, accepted[0]),
			}}
		}
	}
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, badRequest(fmt.Sprintf("body larger than the %d byte limit", maxBody))
		}
		return nil, badRequest("failed to read the body: " + err.Error())
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, badRequest("missing body")
	}
	return b, nil
}

// decodeJSON decodes the JSON body into v, with the shape rules of the
// documents: an unknown field, or one of the wrong type, answers 422 and
// names the field. Malformed JSON answers 400.
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

// malformed turns the JSON syntax error, already located, into a 400.
func malformed(err error) error {
	body := apiError{Error: "bad_request", Message: "malformed JSON"}
	var es config.Errors
	if errors.As(err, &es) && len(es) > 0 {
		body.Message = es[0].Error()
		body.File, body.Line, body.Column = es[0].File, es[0].Line, es[0].Column
	}
	return &requestError{http.StatusBadRequest, body}
}

// readPatch reads a JSON Merge Patch (RFC 7396), which has to be an object.
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
		return nil, badRequest("the merge patch has to be a JSON object")
	}
	return p, nil
}

// applyPatch applies the merge patch over cur and decodes the result into
// out, with the same shape rules as a full write.
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

// mergePatch applies patch over target as per RFC 7396: keys that are
// present replace, null removes, objects merge recursively.
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
