package main

import (
	"strings"
	"testing"
)

const patchENG1 = "PATCH /api/v2/workspaces/acme/projects/ENG/work-items/ENG-1/"

// closeRoutes is a parent ENG-1 with one sub-item ENG-2 in the given state group.
func closeRoutes(childGroup string) map[string]string {
	children := `{"data":[],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`
	if childGroup != "" {
		children = `{"data":[{"id":"c2","project_id":"P","sequence_id":2,"state":{"group":"` + childGroup + `"}}],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`
	}
	return map[string]string{
		"GET /api/v2/workspaces/acme/projects/ENG/states/": `{"data":[{"id":"s-doing","name":"In Progress","group":"started"},` +
			`{"id":"s-done","name":"Done","group":"completed"}],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`,
		"GET /api/v2/workspaces/acme/work-items/ENG-1/": `{"id":"p1","project_id":"P","sequence_id":1}`,
		"GET /api/v2/workspaces/acme/projects/P/":       `{"identifier":"ENG"}`,
		"GET /api/v2/workspaces/acme/work-items/":       children,
		patchENG1: `{"id":"p1"}`,
	}
}

func patched(reqs []recorded) bool {
	for _, r := range reqs {
		if r.Method+" "+r.Path == patchENG1 {
			return true
		}
	}
	return false
}

func TestCloseGuardRefusesParentWithOpenSubItem(t *testing.T) {
	for _, flag := range [][]string{{"--state", "Done"}, {"--state-id", "s-done"}} {
		reqs := fakePlane(t, closeRoutes("started"))
		err := runCLI(t, append([]string{"wi", "update", "ENG-1"}, flag...)...)
		if err == nil || err.Error() != "ENG-1 has 1 open sub-item: ENG-2. Close it first, or add --force" {
			t.Fatalf("%v: err = %v", flag, err)
		}
		if patched(*reqs) {
			t.Errorf("%v: sent the PATCH", flag)
		}
	}
}

func TestCloseGuardForce(t *testing.T) {
	reqs := fakePlane(t, closeRoutes("started"))
	if err := runCLI(t, "wi", "update", "ENG-1", "--state", "Done", "--force"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 || !patched(*reqs) {
		t.Errorf("want only the PATCH, got %v", *reqs)
	}
}

func TestCloseGuardLetsCloseWhenSubItemsAreClosed(t *testing.T) {
	for _, group := range []string{"completed", "cancelled", ""} {
		reqs := fakePlane(t, closeRoutes(group))
		if err := runCLI(t, "wi", "update", "ENG-1", "--state", "Done"); err != nil {
			t.Fatalf("%q: %v", group, err)
		}
		if !patched(*reqs) {
			t.Errorf("%q: no PATCH", group)
		}
	}
}

func TestCloseGuardSkipsOpenStates(t *testing.T) {
	reqs := fakePlane(t, closeRoutes("started"))
	if err := runCLI(t, "wi", "update", "ENG-1", "--state", "In Progress"); err != nil {
		t.Fatal(err)
	}
	for _, r := range *reqs {
		if r.Path == "/api/v2/workspaces/acme/work-items/" {
			t.Error("listed sub-items for an open state")
		}
	}
	if !patched(*reqs) {
		t.Error("no PATCH")
	}
}

func TestCloseGuardWalksAllLevels(t *testing.T) {
	routes := closeRoutes("completed")
	// ENG-2 is closed, but the second level holds the same closed row; the walk stops on seen ids.
	reqs := fakePlane(t, routes)
	if err := runCLI(t, "wi", "update", "ENG-1", "--state", "Done"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range *reqs {
		if r.Path == "/api/v2/workspaces/acme/work-items/" {
			n++
			if !strings.Contains(r.Query, "parent_id__in=") {
				t.Errorf("query = %s", r.Query)
			}
		}
	}
	if n != 2 {
		t.Errorf("sub-item lists = %d, want 2 (one for each level)", n)
	}
}
