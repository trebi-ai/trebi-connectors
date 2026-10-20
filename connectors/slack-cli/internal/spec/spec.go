// Package spec holds the Slack Web API method table that
// scripts/gen-spec.py makes from the API reference.
package spec

import (
	_ "embed"
	"encoding/json"
)

//go:embed spec.json
var raw []byte

// Spec is the full method table.
type Spec struct {
	Nouns   map[string]string `json:"nouns"`
	Methods []Method          `json:"methods"`
}

// Method is one Web API method.
type Method struct {
	Name      string              `json:"name"`
	Path      []string            `json:"path"`
	Summary   string              `json:"summary"`
	HTTP      string              `json:"http"`
	RateLimit string              `json:"rate_limit"`
	Scopes    map[string][]string `json:"scopes"`
	Doc       string              `json:"doc"`
	Note      string              `json:"note"`
	Params    []Param             `json:"params"`
}

// Param types.
const (
	TypeString  = "string"
	TypeBoolean = "boolean"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeJSON    = "json" // JSON object or array, sent as a JSON string
	TypeCSV     = "csv"  // list, sent comma-separated
)

// Param is one method argument.
type Param struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Desc     string   `json:"desc"`
	Example  string   `json:"example,omitempty"`
	Default  string   `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// Load parses the embedded spec.
func Load() (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Find returns the method with the given name (for example chat.postMessage).
func (s *Spec) Find(name string) (Method, bool) {
	for _, m := range s.Methods {
		if m.Name == name {
			return m, true
		}
	}
	return Method{}, false
}

// Param returns the named param.
func (m Method) Param(name string) (Param, bool) {
	for _, p := range m.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Paginated reports whether the method takes a cursor.
func (m Method) Paginated() bool {
	_, ok := m.Param("cursor")
	return ok
}
