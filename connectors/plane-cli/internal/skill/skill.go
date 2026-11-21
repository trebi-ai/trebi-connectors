// Package skill holds the plane-cli reference (SKILL.md) and prints it as
// Markdown. catalog/plane/skill/SKILL.md is the file that people edit. This
// folder keeps a byte-identical copy, because go:embed cannot read a file
// outside the module.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

//go:generate cp ../../../../catalog/plane/skill/SKILL.md SKILL.md

//go:embed SKILL.md
var raw []byte

// The markers of a block that managed mode leaves out. Each is alone on its line.
const (
	OmitStart = "<!-- managed:omit -->"
	OmitEnd   = "<!-- /managed:omit -->"
)

// Raw gives a copy of the embedded file, with the frontmatter and the markers.
func Raw() []byte { return append([]byte(nil), raw...) }

// Render gives the reference without the frontmatter. With managed, it also
// leaves out the managed:omit blocks.
func Render(managed bool) (string, error) { return render(raw, managed) }

func render(src []byte, managed bool) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && lines[0] == "---" {
		i = 1
		for i < len(lines) && lines[i] != "---" {
			i++
		}
		if i == len(lines) {
			return "", errors.New("SKILL.md: the frontmatter is not closed")
		}
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}

	var out []string
	inBlock, skipBlank := false, false
	start := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if skipBlank {
			skipBlank = false
			if trimmed == "" {
				continue
			}
		}
		switch {
		case trimmed == OmitStart:
			if inBlock {
				return "", fmt.Errorf("SKILL.md line %d: %s is inside a managed:omit block", i+1, OmitStart)
			}
			inBlock, start, skipBlank = true, i+1, true
			continue
		case trimmed == OmitEnd:
			if !inBlock {
				return "", fmt.Errorf("SKILL.md line %d: %s has no start", i+1, OmitEnd)
			}
			inBlock, skipBlank = false, true
			continue
		case strings.HasPrefix(trimmed, "<!-- managed:") || strings.HasPrefix(trimmed, "<!-- /managed:"):
			return "", fmt.Errorf("SKILL.md line %d: unknown marker %q", i+1, trimmed)
		}
		if inBlock && managed {
			continue
		}
		out = append(out, line)
	}
	if inBlock {
		return "", fmt.Errorf("SKILL.md line %d: %s has no end", start, OmitStart)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n \t") + "\n", nil
}
