// Package textutil holds small text helpers shared by modules.
package textutil

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a (Vietnamese) title into an ASCII url slug of at most 150 characters.
func Slugify(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, _ := transform.String(t, strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "đ", "d"), "Đ", "d")))
	out = strings.Trim(nonSlug.ReplaceAllString(out, "-"), "-")
	if len(out) > 150 {
		out = strings.Trim(out[:150], "-")
	}
	return out
}
