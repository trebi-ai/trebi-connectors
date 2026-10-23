package vnc

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// X keysyms (X11 keysymdef.h) for the keys that are not characters.
const (
	KeyBackSpace  uint32 = 0xff08
	KeyTab        uint32 = 0xff09
	KeyReturn     uint32 = 0xff0d
	KeyPause      uint32 = 0xff13
	KeyScrollLock uint32 = 0xff14
	KeySysReq     uint32 = 0xff15
	KeyEscape     uint32 = 0xff1b
	KeyHome       uint32 = 0xff50
	KeyLeft       uint32 = 0xff51
	KeyUp         uint32 = 0xff52
	KeyRight      uint32 = 0xff53
	KeyDown       uint32 = 0xff54
	KeyPageUp     uint32 = 0xff55
	KeyPageDown   uint32 = 0xff56
	KeyEnd        uint32 = 0xff57
	KeyInsert     uint32 = 0xff63
	KeyMenu       uint32 = 0xff67
	KeyNumLock    uint32 = 0xff7f
	KeyKPEnter    uint32 = 0xff8d
	KeyKP0        uint32 = 0xffb0
	KeyF1         uint32 = 0xffbe
	KeyShiftL     uint32 = 0xffe1
	KeyShiftR     uint32 = 0xffe2
	KeyControlL   uint32 = 0xffe3
	KeyControlR   uint32 = 0xffe4
	KeyCapsLock   uint32 = 0xffe5
	KeyMetaL      uint32 = 0xffe7
	KeyMetaR      uint32 = 0xffe8
	KeyAltL       uint32 = 0xffe9
	KeyAltR       uint32 = 0xffea
	KeySuperL     uint32 = 0xffeb
	KeySuperR     uint32 = 0xffec
	KeyHyperL     uint32 = 0xffed
	KeyHyperR     uint32 = 0xffee
	KeyDelete     uint32 = 0xffff
)

// KeyNames is the vncdotool KEYMAP (vncdotool/keys.py), plus a few aliases.
var KeyNames = map[string]uint32{
	"bsp": KeyBackSpace, "backspace": KeyBackSpace,
	"tab":    KeyTab,
	"return": KeyReturn, "enter": KeyReturn,
	"esc": KeyEscape, "escape": KeyEscape,
	"ins": KeyInsert, "insert": KeyInsert,
	"delete": KeyDelete, "del": KeyDelete,
	"home": KeyHome, "end": KeyEnd,
	"pgup": KeyPageUp, "pgdn": KeyPageDown,
	"left": KeyLeft, "up": KeyUp, "right": KeyRight, "down": KeyDown,
	"slash": '/', "bslash": '\\', "fslash": '/',
	"spacebar": ' ', "space": ' ', "sb": ' ',
	"lshift": KeyShiftL, "shift": KeyShiftL, "rshift": KeyShiftR,
	"lctrl": KeyControlL, "ctrl": KeyControlL, "rctrl": KeyControlR,
	"lmeta": KeyMetaL, "meta": KeyMetaL, "rmeta": KeyMetaR,
	"lalt": KeyAltL, "alt": KeyAltL, "ralt": KeyAltR,
	"scrlk": KeyScrollLock, "sysrq": KeySysReq, "numlk": KeyNumLock, "caplk": KeyCapsLock,
	"pause": KeyPause, "menu": KeyMenu,
	"lsuper": KeySuperL, "super": KeySuperL, "rsuper": KeySuperR,
	"win": KeySuperL, "cmd": KeySuperL,
	"lhyper": KeyHyperL, "hyper": KeyHyperL, "rhyper": KeyHyperR,
	"kpenter": KeyKPEnter,
	"minus":   '-',
}

func init() {
	for i := uint32(0); i < 20; i++ {
		KeyNames["f"+strconv.Itoa(int(i)+1)] = KeyF1 + i
	}
	for i := uint32(0); i < 10; i++ {
		KeyNames["kp"+strconv.Itoa(int(i))] = KeyKP0 + i
	}
}

// shiftedUS are the characters that need shift on a US layout
// (vncdotool SPECIAL_KEYS_US).
const shiftedUS = `~!@#$%^&*()_+{}|:"<>?`

// ParseKey reads a key or a combo such as "ctrl-alt-del", "a", "f5", or
// "0xff0d". A single character is always that character, so "-" is the
// minus key. With forceCaps, an upper case letter or a shifted symbol gets
// shift, as vncdotool --force-caps does.
func ParseKey(key string, forceCaps bool) ([]uint32, error) {
	if key == "" {
		return nil, fmt.Errorf("empty key")
	}
	if r := []rune(key); len(r) == 1 {
		k := RuneKeysym(r[0])
		if forceCaps && (unicode.IsUpper(r[0]) || strings.ContainsRune(shiftedUS, r[0])) {
			return []uint32{KeyShiftL, k}, nil
		}
		return []uint32{k}, nil
	}
	parts := strings.Split(key, "-")
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		k, err := keysymOf(p)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		out = append(out, k)
	}
	return out, nil
}

func keysymOf(name string) (uint32, error) {
	if r := []rune(name); len(r) == 1 {
		return RuneKeysym(r[0]), nil
	}
	if k, ok := KeyNames[strings.ToLower(name)]; ok {
		return k, nil
	}
	if strings.HasPrefix(name, "0x") {
		if v, err := strconv.ParseUint(name[2:], 16, 32); err == nil {
			return uint32(v), nil
		}
	}
	return 0, fmt.Errorf("unknown key name %q", name)
}

// RuneKeysym maps a character to its keysym: Latin-1 directly, other
// characters as Unicode keysyms (0x01000000 + code point).
func RuneKeysym(r rune) uint32 {
	switch r {
	case '\n', '\r':
		return KeyReturn
	case '\t':
		return KeyTab
	case '\b':
		return KeyBackSpace
	}
	if r >= 0x20 && r <= 0x7e || r >= 0xa0 && r <= 0xff {
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}
