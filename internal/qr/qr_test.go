package qr

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestEncode checks codes against libqrencode's (qrencode -l M -8 -t ASCII
// -m 0) in testdata: the data, its error correction, the layout, and the
// format and version information must all match under one mask. zbarimg
// read every code here, under every mask, when this was written.
func TestEncode(t *testing.T) {
	for file, text := range map[string]string{
		"v1.txt": "https://x",
		"v5.txt": "https://laptop.tail1234.ts.net/#pair=ABCDEFGHJKMNPQRSTVWXYZ2345",
		"v9.txt": "https://desktop-7.tailnet-name.ts.net/#pair=ABCDEFGHJKMNPQRSTVWXYZ2345&with=a-much-longer-fragment-that-pushes-this-into-version-seven-or-beyond-0123456789",
	} {
		data, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			want = append(want, strings.TrimRight(l, " "))
		}
		c, err := Encode(text)
		if err != nil {
			t.Fatal(err)
		}
		ver := (len(c) - 17) / 4
		if len(c) != len(want) || "v"+string(rune('0'+ver))+".txt" != file {
			t.Fatalf("%s: version %d, %d rows, want %d", file, ver, len(c), len(want))
		}
		q := newSymbol(ver)
		q.place(codewords(ver, text))
		found := false
		for mask := range 8 {
			found = found || draw(q.finish(mask)) == strings.Join(want, "\n")
		}
		if !found {
			t.Errorf("%s: matches libqrencode under no mask", file)
		}
	}
	if _, err := Encode(strings.Repeat("x", 213)); err != nil {
		t.Errorf("213 bytes: %v", err)
	}
	if _, err := Encode(strings.Repeat("x", 214)); !errors.Is(err, ErrTooLong) {
		t.Errorf("214 bytes: %v", err)
	}
}

// draw is c as qrencode's ASCII output: ## dark, two spaces light.
func draw(c Code) string {
	var rows []string
	for _, row := range c {
		var b strings.Builder
		for _, dark := range row {
			if dark {
				b.WriteString("##")
			} else {
				b.WriteString("  ")
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(rows, "\n")
}
