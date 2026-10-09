package app

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
)

// toastTag is the Windows toast tag for a notification group, a tab's
// workspace id: Windows 10 before 1703 takes 16 characters, later ones 64.
func toastTag(group string) string { return group[:min(len(group), 16)] }

// toastLine is one toast for the Windows toaster (notify_windows.go): a
// JSON line in plain ASCII, since PowerShell reads its stdin in the
// console's code page, not UTF-8.
func toastLine(title, body, tag string) []byte {
	b, _ := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Tag   string `json:"tag"`
	}{title, body, tag})
	var out []byte
	for _, r := range string(b) {
		if r < 0x80 {
			out = append(out, byte(r))
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			out = fmt.Appendf(out, `\u%04x`, u)
		}
	}
	return append(out, '\n')
}
