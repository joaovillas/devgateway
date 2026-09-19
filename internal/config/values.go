package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration is a time.Duration serialized as text ("150ms", "2s").
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func parseDuration(s string) (Duration, error) {
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use, for example, 150ms or 2s)", s)
	}
	return Duration(v), nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be text, such as \"2s\"")
	}
	v, err := parseDuration(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return nodeErr(n, "duration must be text, such as 2s")
	}
	v, err := parseDuration(n.Value)
	if err != nil {
		return nodeErr(n, "%v", err)
	}
	*d = v
	return nil
}

// Latency is a fixed delay ("2s") or a randomized range ({min, max}).
type Latency struct {
	Fixed *Duration `json:"-" yaml:"-"`
	Min   *Duration `json:"min,omitempty" yaml:"min,omitempty"`
	Max   *Duration `json:"max,omitempty" yaml:"max,omitempty"`
}

type latencyRange struct {
	Min *Duration `json:"min" yaml:"min"`
	Max *Duration `json:"max" yaml:"max"`
}

func (l Latency) MarshalJSON() ([]byte, error) {
	if l.Fixed != nil {
		return json.Marshal(l.Fixed)
	}
	return json.Marshal(latencyRange{l.Min, l.Max})
}

func (l *Latency) UnmarshalJSON(b []byte) error {
	var d Duration
	if json.Unmarshal(b, &d) == nil {
		*l = Latency{Fixed: &d}
		return nil
	}
	var r latencyRange
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return fmt.Errorf("latency must be a duration, such as \"2s\", or {\"min\", \"max\"}: %v", err)
	}
	*l = Latency{Min: r.Min, Max: r.Max}
	return nil
}

func (l Latency) MarshalYAML() (any, error) {
	if l.Fixed != nil {
		return l.Fixed.String(), nil
	}
	return latencyRange{l.Min, l.Max}, nil
}

func (l *Latency) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var d Duration
		if err := d.UnmarshalYAML(n); err != nil {
			return err
		}
		*l = Latency{Fixed: &d}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return nodeErr(n, "latency must be a duration, such as 2s, or a map with min and max")
	}
	var r latencyRange
	if err := n.Decode(&r); err != nil {
		return err
	}
	*l = Latency{Min: r.Min, Max: r.Max}
	return nil
}

// Matcher compares a value from the request using exactly one operator. In
// the short form, plain text means {equals: text}.
type Matcher struct {
	Equals   *string `json:"equals,omitempty" yaml:"equals,omitempty"`
	Regex    *string `json:"regex,omitempty" yaml:"regex,omitempty"`
	JSON     any     `json:"json,omitempty" yaml:"json,omitempty"`
	Contains *string `json:"contains,omitempty" yaml:"contains,omitempty"`
}

type matcherFields Matcher

func (m Matcher) onlyEquals() bool {
	return m.Equals != nil && m.Regex == nil && m.JSON == nil && m.Contains == nil
}

func (m Matcher) MarshalJSON() ([]byte, error) {
	if m.onlyEquals() {
		return json.Marshal(*m.Equals)
	}
	return json.Marshal(matcherFields(m))
}

func (m *Matcher) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*m = Matcher{Equals: &s}
		return nil
	}
	var f matcherFields
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("a criterion must be text or a map with equals, regex, json or contains: %v", err)
	}
	*m = Matcher(f)
	return nil
}

func (m Matcher) MarshalYAML() (any, error) {
	if m.onlyEquals() {
		return *m.Equals, nil
	}
	return matcherFields(m), nil
}

func (m *Matcher) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s := n.Value
		*m = Matcher{Equals: &s}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return nodeErr(n, "a criterion must be text or a map with equals, regex, json or contains")
	}
	var f matcherFields
	if err := n.Decode(&f); err != nil {
		return err
	}
	*m = Matcher(f)
	return nil
}

// nodeErr builds an error in the same "line N:" format the YAML decoder uses,
// so that the translation into a validation error still locates the field.
func nodeErr(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", n.Line, fmt.Sprintf(format, args...))
}

// HeaderValues are the values of a header in the declared response. In the
// short form, plain text is a single value; a list declares the header
// repeated, once per value, as several Set-Cookie would be.
type HeaderValues []string

func (v HeaderValues) MarshalJSON() ([]byte, error) {
	if len(v) == 1 {
		return json.Marshal(v[0])
	}
	return json.Marshal([]string(v))
}

func (v *HeaderValues) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*v = HeaderValues{s}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("a header value must be text or a list of texts")
	}
	*v = l
	return nil
}

func (v HeaderValues) MarshalYAML() (any, error) {
	if len(v) == 1 {
		return v[0], nil
	}
	return []string(v), nil
}

func (v *HeaderValues) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*v = HeaderValues{n.Value}
		return nil
	case yaml.SequenceNode:
		l := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return nodeErr(item, "a header value must be text")
			}
			l = append(l, item.Value)
		}
		*v = l
		return nil
	}
	return nodeErr(n, "a header value must be text or a list of texts")
}
