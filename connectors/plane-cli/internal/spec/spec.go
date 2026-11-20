// Package spec holds the Plane API endpoint table generated from the docs by
// scripts/gen-spec.py.
package spec

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed spec.json
var raw []byte

// Spec is the full command table.
type Spec struct {
	Nouns    map[string]string `json:"nouns"`
	Commands []Command         `json:"commands"`
}

// Command is one CLI leaf. It has one endpoint, or two when the same verb exists
// at workspace and project scope.
type Command struct {
	Path     []string   `json:"path"`
	Variants []Endpoint `json:"variants"`
}

// Endpoint is one documented API call.
type Endpoint struct {
	Version string  `json:"version"`
	Method  string  `json:"method"`
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
	Doc     string  `json:"doc"`
	Params  []Param `json:"params"`
}

// Param is a path, query, or body parameter.
type Param struct {
	Name     string   `json:"name"`
	In       string   `json:"in"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Desc     string   `json:"desc"`
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

// ProjectScoped reports whether the endpoint path contains a project.
func (e Endpoint) ProjectScoped() bool { return strings.Contains(e.Path, "{project_id}") }

// PathParams returns the path placeholders in path order.
func (e Endpoint) PathParams() []string {
	var out []string
	for _, seg := range strings.Split(e.Path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}

// Param returns the named parameter in the given location.
func (e Endpoint) Param(in, name string) (Param, bool) {
	for _, p := range e.Params {
		if p.In == in && p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// IsArray reports whether the param takes a list.
func (p Param) IsArray() bool {
	return strings.HasPrefix(p.Type, "array") && p.Type != "array of object"
}

// IsJSON reports whether the param takes a JSON value.
func (p Param) IsJSON() bool {
	return p.Type == "any" || p.Type == "object" || p.Type == "array of object"
}

// jsonStrings are body fields that the docs mark "string" but that hold JSON.
var jsonStrings = map[string]bool{
	"filters": true, "display_filters": true, "display_properties": true, "pql_filters": true,
	"logo_props": true, "view_props": true, "config": true, "settings": true, "metadata": true,
}

// MaybeJSON reports whether a string value that is a JSON object or array goes as JSON.
func (p Param) MaybeJSON() bool { return p.In == "body" && p.Type == "string" && jsonStrings[p.Name] }

// IsBool reports whether the param is a boolean.
func (p Param) IsBool() bool { return p.Type == "boolean" }
