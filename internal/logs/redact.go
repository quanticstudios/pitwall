package logs

import "regexp"

// Redacted replaces every secret Redact finds.
const Redacted = "[redacted]"

// secretName is a variable or field name that usually holds a secret.
const secretName = `[A-Za-z0-9_.-]*(?:SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|API_?KEY|APIKEY|ACCESS_?KEY|PRIVATE_?KEY|CREDENTIALS?|AUTH|COOKIE|SESSION_?ID|CLIENT_?SECRET|DSN)[A-Za-z0-9_.-]*`

// patterns are applied in order; each replaces its match, or the groups
// named "v" in it, with Redacted.
var patterns = []*regexp.Regexp{
	// PEM private keys, closed or cut off.
	regexp.MustCompile(`-----BEGIN[A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----(?s:.*?)(-----END[A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|$)`),
	// Authorization and cookie headers: the rest of the line.
	regexp.MustCompile(`(?i)\b(?:proxy-)?(?:authorization|cookie|set-cookie|x-api-key|api-key)\s*:\s*(?P<v>[^\r\n]+)`),
	// Bearer and Basic tokens anywhere.
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+(?P<v>[A-Za-z0-9._~+/=-]{8,})`),
	// KEY=value and "key": "value" with a secret-looking name. A quoted
	// value may span lines and may be cut off.
	regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])["']?` + secretName + `["']?\s*(?:=|:=|:)\s*(?P<v>"(?:[^"\\]|\\[\s\S])*(?:"|\z)|'[^']*(?:'|\z)|[^\s"',;}]+)`),
	// Command-line flags such as --password=x or --token x.
	regexp.MustCompile(`(?i)--?(?:password|passwd|token|secret|api-key|apikey|access-key|auth)(?:=|\s+)(?P<v>"(?:[^"\\]|\\[\s\S])*(?:"|\z)|'[^']*(?:'|\z)|[^\s"']+)`),
	// Credentials in URLs.
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]*:(?P<v>[^\s/@]+)@`),
	// Well-known token shapes.
	regexp.MustCompile(`\b(?:sk|pk|rk)-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
	regexp.MustCompile(`\bey[JK][A-Za-z0-9_-]{10,}\.ey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\bts[-_](?:live|test|key)?[-_]?[A-Za-z0-9]{24,}`),
}

// Redact replaces secrets in s: private keys, Authorization headers,
// bearer tokens, secret-named KEY=value pairs and flags, URL passwords,
// common API token shapes.
// ponytail: patterns, not proof; a secret in no known shape and under an
// innocent name passes. An entropy check would catch more and redact more
// that is not secret.
func Redact(s string) string {
	for _, re := range patterns {
		v := re.SubexpIndex("v")
		if v < 0 {
			s = re.ReplaceAllString(s, Redacted)
			continue
		}
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			loc := re.FindStringSubmatchIndex(m)
			if loc == nil || loc[2*v] < 0 {
				return m
			}
			return m[:loc[2*v]] + Redacted + m[loc[2*v+1]:]
		})
	}
	return s
}
