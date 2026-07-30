package store

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// envTemplateRe matches a generator placeholder inside a preset env value, e.g.
// VNC_PW={{random_password:16}} or USER_CODE={{sequence:4}}. Each container built
// from the image resolves these into a fresh concrete value while the admin-facing
// preset keeps the reusable template.
var envTemplateRe = regexp.MustCompile(`\{\{\s*(random_password|random_number|random_string|sequence)(?::(\d+))?\s*\}\}`)

// anyBraceRe matches any {{...}} span, used to catch typos (unknown generator name,
// missing colon) that envTemplateRe wouldn't recognize as a valid placeholder.
var anyBraceRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

const (
	defaultPasswordLen = 16
	defaultNumberLen   = 6
	defaultStringLen   = 12
	defaultSeqWidth    = 4
	maxGeneratedLen    = 128
)

var (
	passwordAlphabet = []rune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#%^&*-_+")
	plainAlphabet    = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	digitAlphabet    = []rune("0123456789")
)

// ValidateEnvTemplates rejects preset env entries whose {{...}} placeholders don't
// match a known generator or carry an out-of-range length, so an admin gets
// immediate feedback on save instead of a literal "{{typo}}" landing in a container.
func ValidateEnvTemplates(env []string) error {
	for _, e := range env {
		valid := envTemplateRe.FindAllString(e, -1)
		all := anyBraceRe.FindAllString(e, -1)
		if len(all) != len(valid) {
			return fmt.Errorf("env entry %q has an unrecognized {{...}} placeholder (expected random_password, random_number, random_string, or sequence)", e)
		}
		for _, m := range envTemplateRe.FindAllStringSubmatch(e, -1) {
			if m[2] == "" {
				continue
			}
			n, err := strconv.Atoi(m[2])
			if err != nil || n <= 0 || n > maxGeneratedLen {
				return fmt.Errorf("env entry %q: placeholder length must be between 1 and %d", e, maxGeneratedLen)
			}
		}
	}
	return nil
}

// ResolveEnvTemplates replaces {{...}} generator placeholders in env with freshly
// generated values (random password/number/string, or the image's next sequence
// number). Entries with no placeholder pass through unchanged. next is called at
// most once, only if a {{sequence}} placeholder is present anywhere in env, so every
// {{sequence}} placeholder in a single resolve shares the same number.
func ResolveEnvTemplates(env []string, next func() (int64, error)) ([]string, error) {
	var seq int64
	for _, e := range env {
		if strings.Contains(e, "{{sequence") {
			var err error
			seq, err = next()
			if err != nil {
				return nil, fmt.Errorf("allocate sequence number: %w", err)
			}
			break
		}
	}

	out := make([]string, len(env))
	for i, e := range env {
		var genErr error
		resolved := envTemplateRe.ReplaceAllStringFunc(e, func(match string) string {
			if genErr != nil {
				return match
			}
			sub := envTemplateRe.FindStringSubmatch(match)
			kind, param := sub[1], sub[2]
			var val string
			var err error
			switch kind {
			case "random_password":
				val, err = generateRandom(passwordAlphabet, atoiOr(param, defaultPasswordLen))
			case "random_number":
				val, err = generateRandom(digitAlphabet, atoiOr(param, defaultNumberLen))
			case "random_string":
				val, err = generateRandom(plainAlphabet, atoiOr(param, defaultStringLen))
			case "sequence":
				val = fmt.Sprintf("%0*d", atoiOr(param, defaultSeqWidth), seq)
			default:
				val = match
			}
			if err != nil {
				genErr = err
				return match
			}
			return val
		})
		if genErr != nil {
			return nil, genErr
		}
		out[i] = resolved
	}
	return out, nil
}

// atoiOr parses s as a placeholder length parameter, falling back to def when s is
// empty or invalid and clamping to maxGeneratedLen.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	if n > maxGeneratedLen {
		return maxGeneratedLen
	}
	return n
}

// generateRandom returns a cryptographically random string of length n drawn from
// alphabet.
func generateRandom(alphabet []rune, n int) (string, error) {
	b := make([]rune, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate random value: %w", err)
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b), nil
}
