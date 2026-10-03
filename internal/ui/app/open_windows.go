package app

import "os/exec"

// openCommand opens url in the user's default handler. rundll32 needs no
// console and, unlike cmd /c start, does not parse & or ^ in the URL.
func openCommand(url string) *exec.Cmd {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
}
