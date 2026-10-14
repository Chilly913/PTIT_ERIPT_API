package catalog

import (
	"strings"
	"unicode"
)

// asciiMap transliterates the letters Str::ascii (voku/portable-ascii, "en")
// maps for Vietnamese and common Latin text. Other non-ASCII letters are
// dropped, like ASCII::to_ascii with remove_unsupported_chars.
var asciiMap = func() map[rune]string {
	m := map[rune]string{}
	add := func(to string, from string) {
		for _, r := range from {
			m[r] = to
		}
	}
	add("a", "àáạảãâầấậẩẫăằắặẳẵäåāąǎ")
	add("A", "ÀÁẠẢÃÂẦẤẬẨẪĂẰẮẶẲẴÄÅĀĄǍ")
	add("e", "èéẹẻẽêềếệểễëēęěė")
	add("E", "ÈÉẸẺẼÊỀẾỆỂỄËĒĘĚĖ")
	add("i", "ìíịỉĩîïīįı")
	add("I", "ÌÍỊỈĨÎÏĪĮİ")
	add("o", "òóọỏõôồốộổỗơờớợởỡöøōő")
	add("O", "ÒÓỌỎÕÔỒỐỘỔỖƠỜỚỢỞỠÖØŌŐ")
	add("u", "ùúụủũưừứựửữûüūůűų")
	add("U", "ÙÚỤỦŨƯỪỨỰỬỮÛÜŪŮŰŲ")
	add("y", "ỳýỵỷỹÿ")
	add("Y", "ỲÝỴỶỸŸ")
	add("d", "đď")
	add("D", "ĐĎ")
	add("c", "çćčĉċ")
	add("C", "ÇĆČĈĊ")
	add("n", "ñńňņ")
	add("N", "ÑŃŇŅ")
	add("s", "śšşș")
	add("S", "ŚŠŞȘ")
	add("z", "źžż")
	add("Z", "ŹŽŻ")
	add("r", "ŕř")
	add("R", "ŔŘ")
	add("l", "ĺľłļ")
	add("L", "ĹĽŁĻ")
	add("t", "ťţț")
	add("T", "ŤŢȚ")
	add("g", "ğĝġģ")
	add("G", "ĞĜĠĢ")
	m['ß'] = "ss"
	m['æ'] = "ae"
	m['Æ'] = "AE"
	m['œ'] = "oe"
	m['Œ'] = "OE"
	return m
}()

// slugify ports Str::slug($title) with the default "-" separator:
// transliterate to ASCII, "_" -> "-", "@" -> "-at-", lowercase, drop every
// character that is not a letter, digit, separator or whitespace, collapse
// runs of separators/whitespace into "-" and trim it.
func slugify(title string) string {
	var ascii strings.Builder
	for _, r := range title {
		switch {
		case r < 0x80:
			ascii.WriteRune(r)
		case unicode.IsSpace(r):
			ascii.WriteByte(' ')
		default:
			ascii.WriteString(asciiMap[r]) // unsupported characters are removed
		}
	}
	s := strings.ReplaceAll(ascii.String(), "_", "-")
	s = strings.ReplaceAll(s, "@", "-at-")
	s = strings.ToLower(s)

	var out strings.Builder
	pendingSep := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingSep && out.Len() > 0 {
				out.WriteByte('-')
			}
			pendingSep = false
			out.WriteRune(r)
		case r == '-' || unicode.IsSpace(r):
			pendingSep = true
		}
	}
	return out.String()
}
