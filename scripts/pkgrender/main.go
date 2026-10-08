// Command pkgrender writes the Homebrew formula, Scoop manifest and AUR
// pitwall-bin files for a release from its checksums.txt:
//
//	go run ./scripts/pkgrender -tag v0.1.0-alpha.24 -sums checksums.txt -out pkg
package main

import (
	"bufio"
	"bytes"
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed tmpl
var tmplFS embed.FS

// outputs maps each template to the file it renders.
var outputs = map[string]string{
	"pitwall.rb":   "pitwall.rb",
	"pitwall.json": "pitwall.json",
	"PKGBUILD":     "PKGBUILD",
	"SRCINFO":      ".SRCINFO",
}

// tagRE is a release tag; it also keeps the tag safe to paste into Ruby,
// JSON and shell.
var tagRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func main() {
	tag := flag.String("tag", "", "release tag, such as v0.1.0-alpha.24")
	sums := flag.String("sums", "", "the release's checksums.txt")
	out := flag.String("out", ".", "directory to write the files to")
	flag.Parse()
	data, err := os.ReadFile(*sums)
	if err == nil {
		err = run(*tag, data, *out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pkgrender:", err)
		os.Exit(1)
	}
}

func run(tag string, sums []byte, out string) error {
	files, err := render(tag, sums)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// render returns each output file's contents by name.
func render(tag string, sums []byte) (map[string][]byte, error) {
	if !tagRE.MatchString(tag) {
		return nil, fmt.Errorf("tag %q is not vX.Y.Z or vX.Y.Z-pre", tag)
	}
	hashes := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && hashRE.MatchString(f[0]) {
			hashes[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	version := strings.TrimPrefix(tag, "v")
	data := struct{ Tag, Version, PkgVer string }{
		Tag:     tag,
		Version: version,
		// why: pacman's vercmp ranks 0.1.0alpha.1 below 0.1.0, but reads a
		// separator as in 0.1.0_alpha.1 as a newer point release.
		PkgVer: strings.ReplaceAll(version, "-", ""),
	}
	funcs := template.FuncMap{"sum": func(asset string) (string, error) {
		if h, ok := hashes[asset]; ok {
			return h, nil
		}
		return "", fmt.Errorf("checksums.txt has no %s", asset)
	}}
	files := map[string][]byte{}
	for src, dst := range outputs {
		t, err := template.New(src).Funcs(funcs).ParseFS(tmplFS, "tmpl/"+src)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return nil, err
		}
		files[dst] = b.Bytes()
	}
	return files, nil
}
