package serve

import (
	"fmt"
	"slices"
	"strings"
)

// Scope is the parsed PLANE_PROJECTS value: workspace slug → project
// identifiers. A nil list is every project of the workspace.
type Scope map[string][]string

// ParseScope reads "acme/*, beta/ENG". Commas, spaces, and new
// lines separate the entries. A bare slug is the same as "<slug>/*".
func ParseScope(s string) (Scope, error) {
	sc := Scope{}
	all := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		ws, proj, _ := strings.Cut(f, "/")
		ws = strings.TrimSpace(ws)
		proj = strings.ToUpper(strings.TrimSpace(proj))
		if ws == "" || strings.Contains(proj, "/") {
			return nil, fmt.Errorf("bad entry %q: use <workspace>/<project> or <workspace>/*", f)
		}
		if proj == "" || proj == "*" {
			all[ws] = true
			sc[ws] = nil
			continue
		}
		if !all[ws] && !slices.Contains(sc[ws], proj) {
			sc[ws] = append(sc[ws], proj)
		}
	}
	if len(sc) == 0 {
		return nil, fmt.Errorf("no workspace in %q", s)
	}
	return sc, nil
}

// Workspaces returns the slugs in sorted order.
func (s Scope) Workspaces() []string {
	out := make([]string, 0, len(s))
	for ws := range s {
		out = append(out, ws)
	}
	slices.Sort(out)
	return out
}

// All reports whether the scope has every project of ws.
func (s Scope) All(ws string) bool {
	p, ok := s[ws]
	return ok && p == nil
}

// Has reports whether the project identifier of ws is in the scope.
func (s Scope) Has(ws, ident string) bool {
	p, ok := s[ws]
	return ok && (p == nil || slices.Contains(p, strings.ToUpper(ident)))
}
