package vnc

import (
	"slices"
	"testing"
)

func TestParseKey(t *testing.T) {
	for in, want := range map[string][]uint32{
		"a":            {'a'},
		"-":            {'-'},
		"enter":        {KeyReturn},
		"Return":       {KeyReturn},
		"ctrl-alt-del": {KeyControlL, KeyAltL, KeyDelete},
		"shift-a":      {KeyShiftL, 'a'},
		"ctrl-minus":   {KeyControlL, '-'},
		"f1":           {KeyF1},
		"f20":          {KeyF1 + 19},
		"kp9":          {KeyKP0 + 9},
		"super-l":      {KeySuperL, 'l'},
		"0xff0d":       {KeyReturn},
		"é":            {0xe9},
		"☃":            {0x01002603},
	} {
		got, err := ParseKey(in, false)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: got %x, %v; want %x", in, got, err, want)
		}
	}
	if _, err := ParseKey("ctrl-nokey", false); err == nil {
		t.Error("unknown name gave no error")
	}
	if got, _ := ParseKey("A", true); !slices.Equal(got, []uint32{KeyShiftL, 'A'}) {
		t.Errorf("force caps A: %x", got)
	}
	if got, _ := ParseKey("?", true); !slices.Equal(got, []uint32{KeyShiftL, '?'}) {
		t.Errorf("force caps ?: %x", got)
	}
	if got, _ := ParseKey("a", true); !slices.Equal(got, []uint32{'a'}) {
		t.Errorf("force caps a: %x", got)
	}
}

func TestRuneKeysym(t *testing.T) {
	for r, want := range map[rune]uint32{'\n': KeyReturn, '\t': KeyTab, 'z': 'z', 'ÿ': 0xff, 'Ā': 0x01000100} {
		if got := RuneKeysym(r); got != want {
			t.Errorf("%q: %x, want %x", r, got, want)
		}
	}
}
