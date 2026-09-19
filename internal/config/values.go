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

// Latency is a fixed delay ("2s") or a randomized range ({min, max}), and
// may carry its own frequency in the long form ({fixed, chance} or
// {min, max, chance}). The short form, a bare duration, holds on every
// selected request.
type Latency struct {
	Fixed *Duration `json:"-" yaml:"-"`
	Min   *Duration `json:"min,omitempty" yaml:"min,omitempty"`
	Max   *Duration `json:"max,omitempty" yaml:"max,omitempty"`
	// Chance is how often the delay is injected, between 0.0 and 1.0.
	// Leaving it out means every selected request.
	Chance *float64 `json:"chance,omitempty" yaml:"chance,omitempty"`
}

// latencyFields is the long form of the latency, and the shape the document
// is checked against.
type latencyFields struct {
	Fixed  *Duration `json:"fixed,omitempty" yaml:"fixed,omitempty"`
	Min    *Duration `json:"min,omitempty" yaml:"min,omitempty"`
	Max    *Duration `json:"max,omitempty" yaml:"max,omitempty"`
	Chance *float64  `json:"chance,omitempty" yaml:"chance,omitempty"`
}

func (l Latency) fields() latencyFields {
	return latencyFields{Fixed: l.Fixed, Min: l.Min, Max: l.Max, Chance: l.Chance}
}

// short reports that the latency is a bare duration: a fixed delay with no
// frequency of its own.
func (l Latency) short() bool { return l.Fixed != nil && l.Chance == nil }

const latencyShape = "latency must be a duration, such as 2s, a map with min and max, or a map with fixed or min and max plus chance"

func (l Latency) MarshalJSON() ([]byte, error) {
	if l.short() {
		return json.Marshal(l.Fixed)
	}
	return json.Marshal(l.fields())
}

func (l *Latency) UnmarshalJSON(b []byte) error {
	var d Duration
	if json.Unmarshal(b, &d) == nil {
		*l = Latency{Fixed: &d}
		return nil
	}
	var f latencyFields
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("%s: %v", latencyShape, err)
	}
	*l = Latency{Fixed: f.Fixed, Min: f.Min, Max: f.Max, Chance: f.Chance}
	return nil
}

func (l Latency) MarshalYAML() (any, error) {
	if l.short() {
		return l.Fixed.String(), nil
	}
	return l.fields(), nil
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
		return nodeErr(n, "%s", latencyShape)
	}
	var f latencyFields
	if err := n.Decode(&f); err != nil {
		return err
	}
	*l = Latency{Fixed: f.Fixed, Min: f.Min, Max: f.Max, Chance: f.Chance}
	return nil
}

// Drop is the connection-drop effect. In the short form, "drop: true" drops
// every request the override selects and "drop: false" is the same as not
// declaring it at all; the long form, "drop: {chance: 0.05}", drops that
// fraction of them.
type Drop struct {
	// On reports that the drop is declared. It carries no field of its own in
	// the document: the short form is the boolean itself.
	On bool `json:"-" yaml:"-"`
	// Chance is how often the connection is dropped, between 0.0 and 1.0.
	// Leaving it out means every selected request.
	Chance *float64 `json:"chance,omitempty" yaml:"chance,omitempty"`
}

// Declared reports whether the override declares the drop.
func (d Drop) Declared() bool { return d.On }

type dropFields struct {
	Chance *float64 `json:"chance,omitempty" yaml:"chance,omitempty"`
}

const dropShape = "drop must be true, false or a map with chance"

func (d Drop) MarshalJSON() ([]byte, error) {
	if d.Chance == nil {
		return json.Marshal(d.On)
	}
	return json.Marshal(dropFields{Chance: d.Chance})
}

func (d *Drop) UnmarshalJSON(b []byte) error {
	var on bool
	if json.Unmarshal(b, &on) == nil {
		*d = Drop{On: on}
		return nil
	}
	var f dropFields
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("%s: %v", dropShape, err)
	}
	*d = Drop{On: true, Chance: f.Chance}
	return nil
}

func (d Drop) MarshalYAML() (any, error) {
	if d.Chance == nil {
		return d.On, nil
	}
	return dropFields{Chance: d.Chance}, nil
}

func (d *Drop) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var on bool
		if err := n.Decode(&on); err != nil {
			return nodeErr(n, "%s", dropShape)
		}
		*d = Drop{On: on}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return nodeErr(n, "%s", dropShape)
	}
	var f dropFields
	if err := n.Decode(&f); err != nil {
		return err
	}
	*d = Drop{On: true, Chance: f.Chance}
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
