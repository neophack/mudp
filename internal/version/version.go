// Package version exposes the build identifier shown in the console's help
// page and compared by the update check. Following the openp2p model, the
// constant below is the single source of truth for the version: there is no
// build-time injection, so every build (CI, local, plain `go build`) reports
// exactly this string. The format is vMAJOR.MINOR.PATCH (v1.2.0); bump it in
// the same commit that ships a release, and give the release workflow the
// same value.
package version

import (
	"strconv"
	"strings"
)

var Version = "v1.1.1"

// Compare orders two version strings the way release tags do: an optional
// leading "v", dot-separated numeric segments, and an optional "-" suffix as
// produced by `git describe --tags --always` (v1.0-5-gabc means 5 commits
// after v1.0, i.e. newer than v1.0 itself). Non-numeric segments compare
// lexically. It returns -1 when a < b, 0 when equal, 1 when a > b.
func Compare(a, b string) int {
	// Untagged dev builds are older than every tagged release so the update
	// check never tells a developer to "upgrade" off dev.
	if a == b {
		return 0
	}
	if a == "dev" {
		return -1
	}
	if b == "dev" {
		return 1
	}

	aBase, aSuffix := splitTag(a)
	bBase, bSuffix := splitTag(b)
	n := len(aBase)
	if len(bBase) > n {
		n = len(bBase)
	}
	for i := 0; i < n; i++ {
		x, y := segment(aBase, i), segment(bBase, i)
		if x == y {
			continue
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		if xerr == nil && yerr == nil {
			if xn < yn {
				return -1
			}
			return 1
		}
		if x < y {
			return -1
		}
		return 1
	}
	return compareSuffix(aSuffix, bSuffix)
}

// compareSuffix orders `git describe` suffixes ("<commits>-g<sha>"): more
// commits since the tag means newer, ties fall back to a lexical compare.
func compareSuffix(a, b string) int {
	if a == b {
		return 0
	}
	an, aerr := strconv.Atoi(strings.SplitN(a, "-", 2)[0])
	bn, berr := strconv.Atoi(strings.SplitN(b, "-", 2)[0])
	if aerr == nil && berr == nil && an != bn {
		if an < bn {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func splitTag(v string) (base []string, suffix string) {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		suffix = v[i+1:]
		v = v[:i]
	}
	base = strings.Split(v, ".")
	if len(base) == 1 && base[0] == "" {
		base = []string{"0"}
	}
	return base, suffix
}

func segment(base []string, i int) string {
	if i >= len(base) {
		return "0"
	}
	if base[i] == "" {
		return "0"
	}
	return base[i]
}
