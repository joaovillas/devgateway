package admin

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The examples from RFC 7396, appendix A.
func TestMergePatchRFC7396(t *testing.T) {
	cases := []struct{ target, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{"e":null}`, `{"a":1}`, `{"a":1,"e":null}`},
		{`[1,2]`, `{"a":"b","c":null}`, `{"a":"b"}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	}
	for _, c := range cases {
		var target, patch, want any
		json.Unmarshal([]byte(c.target), &target)
		json.Unmarshal([]byte(c.patch), &patch)
		json.Unmarshal([]byte(c.want), &want)
		if got := mergePatch(target, patch); !reflect.DeepEqual(got, want) {
			t.Errorf("%s + %s: want %v, got %v", c.target, c.patch, want, got)
		}
	}
}
