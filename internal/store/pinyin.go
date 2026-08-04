package store

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// Pinyinize converts a name to a filesystem/URL-friendly slug: Han characters
// become their (tone-free) pinyin, everything else (Latin letters, digits)
// passes through unchanged, and all whitespace is dropped so the result never
// contains a space. Used for Feishu users' pinyinName field and for deriving
// per-user shared-disk folder names from a possibly-Chinese display name.
func Pinyinize(name string) string {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	var b strings.Builder
	for _, r := range name {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.Is(unicode.Han, r) {
			py := pinyin.SinglePinyin(r, args)
			if len(py) > 0 && py[0] != "" {
				b.WriteString(py[0])
				continue
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
