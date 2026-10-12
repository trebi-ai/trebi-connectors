// Package output renders API responses as JSON or tables.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// WriteJSON writes v as indented JSON.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteRaw writes JSON bytes, indented when possible.
func WriteRaw(w io.Writer, body []byte) error {
	var v any
	if json.Unmarshal(body, &v) != nil {
		_, err := w.Write(body)
		return err
	}
	return WriteJSON(w, v)
}

// Table writes rows aligned by tab stops.
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
		under := make([]string, len(headers))
		for i, h := range headers {
			under[i] = strings.Repeat("-", max(len(h), 3))
		}
		fmt.Fprintln(tw, strings.Join(under, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// KV writes a key/value block.
func KV(w io.Writer, pairs [][2]string) error {
	width := 0
	for _, p := range pairs {
		width = max(width, len(p[0]))
	}
	for _, p := range pairs {
		if _, err := fmt.Fprintf(w, "%-*s  %s\n", width, p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// Truncate shortens s to n runes and flattens newlines.
func Truncate(s string, n int) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// preferred lists the columns a table shows first when they exist.
var preferred = []string{
	"id", "name", "real_name", "display_name", "title", "user", "channel",
	"team", "email", "value", "ts", "text", "is_private", "is_archived",
	"num_members", "created",
}

// Rows renders a list of objects as a table. Explicit cols win.
func Rows(w io.Writer, rows []map[string]any, cols []string) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "(none)")
		return err
	}
	cols = pickColumns(rows, cols)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = strings.ToUpper(c)
	}
	out := make([][]string, len(rows))
	for i, r := range rows {
		line := make([]string, len(cols))
		for j, c := range cols {
			line[j] = Truncate(Format(Lookup(r, c)), 60)
		}
		out[i] = line
	}
	return Table(w, headers, out)
}

// Object renders one object as a key/value block, id first.
func Object(w io.Writer, m map[string]any, cols []string) error {
	keys := cols
	if len(keys) == 0 {
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if (keys[i] == "id") != (keys[j] == "id") {
				return keys[i] == "id"
			}
			return keys[i] < keys[j]
		})
	}
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, Truncate(Format(Lookup(m, k)), 200)})
	}
	return KV(w, pairs)
}

func pickColumns(rows []map[string]any, cols []string) []string {
	if len(cols) > 0 {
		return cols
	}
	present := map[string]bool{}
	for _, r := range rows {
		for k, v := range r {
			if isScalar(v) {
				present[k] = true
			}
		}
	}
	var out []string
	for _, k := range preferred {
		if present[k] && len(out) < 6 {
			out = append(out, k)
		}
	}
	if len(out) >= 2 {
		return out
	}
	var rest []string
	for k := range present {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		if len(out) >= 8 {
			break
		}
		if !contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Lookup reads a dotted key such as "profile.email".
func Lookup(m map[string]any, key string) any {
	var cur any = m
	for _, part := range strings.Split(key, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[part]
	}
	return cur
}

// Format renders a JSON value as a short string.
func Format(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case []any:
		if allScalar(t) {
			parts := make([]string, len(t))
			for i, x := range t {
				parts[i] = Format(x)
			}
			return strings.Join(parts, ",")
		}
	case map[string]any:
		for _, k := range []string{"name", "real_name", "display_name", "email", "id"} {
			if s, ok := t[k].(string); ok {
				return s
			}
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func isScalar(v any) bool {
	switch t := v.(type) {
	case nil, string, bool, float64:
		return true
	case []any:
		return allScalar(t)
	}
	return false
}

func allScalar(a []any) bool {
	for _, x := range a {
		switch x.(type) {
		case string, bool, float64, nil:
		default:
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
