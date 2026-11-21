package skill

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestCopyMatchesCatalog(t *testing.T) {
	want, err := os.ReadFile("../../../../catalog/plane/skill/SKILL.md")
	if err != nil {
		t.Fatalf("read the catalog file: %v", err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatal("internal/skill/SKILL.md differs from catalog/plane/skill/SKILL.md; run go generate ./internal/skill")
	}
}

func TestRender(t *testing.T) {
	const src = "---\nname: x\ndescription: y\n---\n\n\n# Title\n\nA\n\n" + OmitStart + "\n\nB\n\n" + OmitEnd + "\n\nC\n\n"
	for _, tc := range []struct {
		name    string
		src     string
		managed bool
		want    string
	}{
		{"standalone", src, false, "# Title\n\nA\n\nB\n\nC\n"},
		{"managed", src, true, "# Title\n\nA\n\nC\n"},
		{"no frontmatter", "A\n" + OmitStart + "\nB\n" + OmitEnd + "\nC", false, "A\nB\nC\n"},
		{"block at the end", "A\n\n" + OmitStart + "\n\nB\n\n" + OmitEnd + "\n", true, "A\n"},
		{"two blocks", "A\n\n" + OmitStart + "\n\nB\n\n" + OmitEnd + "\n\n" + OmitStart + "\n\nC\n\n" + OmitEnd + "\n\nD\n", true, "A\n\nD\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := render([]byte(tc.src), tc.managed)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"end with no start", "A\n" + OmitEnd + "\n", "SKILL.md line 2: " + OmitEnd + " has no start"},
		{"nested start", OmitStart + "\nA\n" + OmitStart + "\n" + OmitEnd + "\n", "SKILL.md line 3: " + OmitStart + " is inside a managed:omit block"},
		{"start with no end", "A\n\n" + OmitStart + "\nB\n", "SKILL.md line 3: " + OmitStart + " has no end"},
		{"unknown marker", "A\n<!-- managed:omitt -->\n", `SKILL.md line 2: unknown marker "<!-- managed:omitt -->"`},
		{"open frontmatter", "---\nname: x\n", "SKILL.md: the frontmatter is not closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, managed := range []bool{false, true} {
				_, err := render([]byte(tc.src), managed)
				if err == nil || err.Error() != tc.want {
					t.Fatalf("managed=%v: got %v, want %q", managed, err, tc.want)
				}
			}
		})
	}
}

func TestEmbeddedSkillRenders(t *testing.T) {
	for _, managed := range []bool{false, true} {
		out, err := Render(managed)
		if err != nil {
			t.Fatalf("managed=%v: %v", managed, err)
		}
		if strings.Contains(out, "<!-- managed") || strings.Contains(out, "<!-- /managed") {
			t.Fatalf("managed=%v: a marker is in the output", managed)
		}
		if strings.HasPrefix(out, "---") || !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
			t.Fatalf("managed=%v: bad start or end", managed)
		}
	}
	out, _ := Render(true)
	for _, s := range []string{"auth login", "config.toml", "PLANE_CONFIG", "--api-key"} {
		if strings.Contains(out, s) {
			t.Errorf("managed output has %q", s)
		}
	}
	for _, s := range []string{"## Identifiers", "Managed mode (OverSire)"} {
		if !strings.Contains(out, s) {
			t.Errorf("managed output has no %q", s)
		}
	}
}
