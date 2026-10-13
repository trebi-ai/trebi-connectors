package output

import (
	"bytes"
	"encoding/json"
	"io"
)

// WriteJSON writes raw JSON to w, pretty-printing unless compact is true.
// Always ends the output with a newline.
func WriteJSON(w io.Writer, raw json.RawMessage, compact bool) error {
	if len(raw) == 0 {
		_, err := w.Write([]byte("null\n"))
		return err
	}
	if compact {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			// Fall back to writing the raw payload if it isn't strictly compactable.
			if _, err2 := w.Write(raw); err2 != nil {
				return err2
			}
			_, err = w.Write([]byte("\n"))
			return err
		}
		buf.WriteByte('\n')
		_, err := buf.WriteTo(w)
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		if _, err2 := w.Write(raw); err2 != nil {
			return err2
		}
		_, err = w.Write([]byte("\n"))
		return err
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(w)
	return err
}
