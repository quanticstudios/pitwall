package logs

import (
	"strings"
	"testing"
)

// TestRedact removes each secret shape from text an agent or a program wrote.
func TestRedact(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWx",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
		"AKIAABCDEFGHIJKLMNOP",
		"supersecretvalue1",
		"pa55word-in-url",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
		"tok3n-in-header-value",
		"xoxb-123456789012-abcdefghij",
		"flagpassword99",
		"ts_live_abcdefghijklmnopqrstuvwxyz012345",
	}
	text := strings.Join([]string{
		"export ANTHROPIC_API_KEY=" + secrets[0] + " && git push https://user:" + secrets[4] + "@example.com/r.git",
		"GITHUB_TOKEN=" + secrets[1],
		"AWS_ACCESS_KEY_ID=" + secrets[2],
		"DB_PASSWORD=\"" + secrets[3] + "\"\nPORT=8080",
		"token " + secrets[5],
		"-----BEGIN RSA PRIVATE KEY-----\n" + secrets[6] + "\n-----END RSA PRIVATE KEY-----",
		"curl -H 'Authorization: Bearer " + secrets[7] + "' https://api.example.com",
		secrets[8],
		"mysql --password=" + secrets[9],
		secrets[10],
	}, "\n")
	got := Redact(text)
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Errorf("Redact left %q in %q", s, got)
		}
	}
	if !strings.Contains(got, "PORT=8080") {
		t.Errorf("Redact took more than the secrets: %q", got)
	}
}

// TestRedactEscapesAndOpenQuotes takes a quoted value whole, past an escaped
// quote or a line break, and when the closing quote is cut off.
func TestRedactEscapesAndOpenQuotes(t *testing.T) {
	for _, in := range []string{
		`API_KEY="first\"remainingsecret"`,
		`{"api_key": "first\"remainingsecret"}`,
		"mysql --password 'first\nremainingsecret",
		`curl --token "first\"remainingsecret"`,
		"API_KEY=\"first\\\nremainingsecret\"",
		"curl --token \"first\\\nremainingsecret\" https://x",
	} {
		if out := Redact(in); strings.Contains(out, "remainingsecret") {
			t.Errorf("Redact(%q) = %q", in, out)
		}
	}
}
