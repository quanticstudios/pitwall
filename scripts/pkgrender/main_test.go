package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the templates")

// TestRender renders v0.1.0-alpha.23's real checksums.txt and compares each
// file with testdata/golden.
func TestRender(t *testing.T) {
	sums, err := os.ReadFile("testdata/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	files, err := render("v0.1.0-alpha.23", sums)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range files {
		path := filepath.Join("testdata", "golden", name)
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; go test ./scripts/pkgrender -update rewrites it\n%s", name, path, got)
		}
	}

	if _, err := render("v0.1.0-alpha.23", sums[:len(sums)/2]); err == nil {
		t.Error("render with assets missing from checksums.txt: no error")
	}
	if _, err := render("v0.1.0'; rm -rf /", sums); err == nil {
		t.Error("render with a tag that is not a version: no error")
	}
}
