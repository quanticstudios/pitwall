package review

import (
	"reflect"
	"strings"
	"testing"
)

// patch is git diff -M --src-prefix=a/ --dst-prefix=b/ output: a
// modification, a rename with a change, a binary file, a deletion, an
// addition and a path git quotes.
const patch = `diff --git a/m.txt b/m.txt
index 1111111..2222222 100644
--- a/m.txt
+++ b/m.txt
@@ -1,3 +1,3 @@
 a
-b
+B
 c
\ No newline at end of file
diff --git a/old name.go b/new name.go
similarity index 80%
rename from old name.go
rename to new name.go
index 3333333..4444444 100644
--- a/old name.go
+++ b/new name.go
@@ -2,2 +2,3 @@ func f() {
 x
+y
 z
diff --git a/bin.dat b/bin.dat
index 5555555..6666666 100644
Binary files a/bin.dat and b/bin.dat differ
diff --git a/del.txt b/del.txt
deleted file mode 100644
index 7777777..0000000
--- a/del.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/add.txt b/add.txt
new file mode 100644
index 0000000..8888888
--- /dev/null
+++ b/add.txt
@@ -0,0 +1,2 @@
+one
+two
diff --git "a/t\303\251\tst" "b/t\303\251\tst"
index 9999999..aaaaaaa 100644
--- "a/t\303\251\tst"
+++ "b/t\303\251\tst"
@@ -1 +1 @@
-x
+y
`

// TestParse checks status, rename and binary detection, the paths, the
// counts and every line's numbers on both sides.
func TestParse(t *testing.T) {
	files := Parse([]byte(patch))
	type got struct {
		Path, OldPath string
		Status        byte
		Binary        bool
		Add, Del      int
	}
	var gs []got
	for _, f := range files {
		gs = append(gs, got{f.Path, f.OldPath, f.Status, f.Binary, f.Add, f.Del})
	}
	want := []got{
		{"m.txt", "", 'M', false, 1, 1},
		{"new name.go", "old name.go", 'R', false, 1, 0},
		{"bin.dat", "", 'M', true, 0, 0},
		{"del.txt", "", 'D', false, 0, 1},
		{"add.txt", "", 'A', false, 2, 0},
		{"té\tst", "", 'M', false, 1, 1},
	}
	if !reflect.DeepEqual(gs, want) {
		t.Fatalf("files:\n got %+v\nwant %+v", gs, want)
	}
	wantM := []Line{{' ', 1, 1, "a"}, {'-', 2, 0, "b"}, {'+', 0, 2, "B"}, {' ', 3, 3, "c"}}
	if !reflect.DeepEqual(files[0].Lines, wantM) {
		t.Errorf("m.txt lines %+v, want %+v", files[0].Lines, wantM)
	}
	wantR := []Line{{' ', 2, 2, "x"}, {'+', 0, 3, "y"}, {' ', 3, 4, "z"}}
	if !reflect.DeepEqual(files[1].Lines, wantR) {
		t.Errorf("rename lines %+v, want %+v", files[1].Lines, wantR)
	}
	if l := files[3].Lines; len(l) != 1 || l[0] != (Line{'-', 1, 0, "gone"}) {
		t.Errorf("deleted lines %+v", l)
	}
	if l := files[4].Lines; len(l) != 2 || l[1] != (Line{'+', 0, 2, "two"}) {
		t.Errorf("added lines %+v", l)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if len(f.Hash) != 16 || seen[f.Hash] {
			t.Errorf("%s: hash %q not unique", f.Path, f.Hash)
		}
		seen[f.Hash] = true
	}
	// The hash follows the file's own change only.
	again := Parse([]byte(strings.Replace(patch, "+y\n z", "+w\n z", 1)))
	if again[0].Hash != files[0].Hash || again[1].Hash == files[1].Hash {
		t.Errorf("a change to the rename moved hashes: m %v rename %v", again[0].Hash == files[0].Hash, again[1].Hash != files[1].Hash)
	}
}

func TestUntracked(t *testing.T) {
	f := Untracked("n.txt", []byte("a\r\nb\n"), false)
	if f.Status != '?' || f.Add != 2 || f.Lines[0] != (Line{'+', 0, 1, "a"}) || f.Lines[1].New != 2 {
		t.Errorf("text: %+v", f)
	}
	if f := Untracked("b", []byte("x\x00y"), false); !f.Binary || len(f.Lines) != 0 {
		t.Errorf("binary: %+v", f)
	}
	if f := Untracked("e", nil, false); f.Binary || len(f.Lines) != 0 {
		t.Errorf("empty: %+v", f)
	}
}

// lines is a file of n unchanged lines with the lines at changed replaced
// by a deletion and an addition.
func lines(n int, changed ...int) []Line {
	var out []Line
	old, nu := 0, 0
	for i := 1; i <= n; i++ {
		old++
		if contains(changed, i) {
			nu++
			out = append(out, Line{'-', old, 0, "old"}, Line{'+', 0, nu, "new"})
			continue
		}
		nu++
		out = append(out, Line{' ', old, nu, "same"})
	}
	return out
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestHunks maps changes to hunks as git -U3 would: changes 6 unchanged
// lines apart share a hunk, 7 apart do not, and the headers count each
// side.
func TestHunks(t *testing.T) {
	ls := lines(30, 5, 12, 20)
	// Line i (1-based) of the old file is index i-1, plus one for each
	// change above it.
	hs := Hunks(ls, 3)
	want := []Hunk{{1, 17}, {18, 26}}
	if !reflect.DeepEqual(hs, want) {
		t.Fatalf("Hunks = %v, want %v", hs, want)
	}
	if got := Header(ls, hs[0]); got != "@@ -2,14 +2,14 @@" {
		t.Errorf("first header %q", got)
	}
	if got := Header(ls, hs[1]); got != "@@ -17,7 +17,7 @@" {
		t.Errorf("second header %q", got)
	}
	// A pure insertion: the old side counts 0 and starts at the line before.
	ins := []Line{{' ', 1, 1, "a"}, {'+', 0, 2, "b"}, {' ', 2, 3, "c"}}
	if got := Header(ins, Hunk{1, 2}); got != "@@ -1,0 +2,1 @@" {
		t.Errorf("insertion header %q", got)
	}
}

// TestRows folds the unchanged runs before, between and after hunks, and
// shows a run once open names its first line.
func TestRows(t *testing.T) {
	ls := lines(30, 5, 20)
	kinds := func(rows []Row) string {
		var b strings.Builder
		for _, r := range rows {
			b.WriteByte(r.Kind)
		}
		return b.String()
	}
	rows := Rows(ls, 3, nil)
	if got := kinds(rows); got != "fh"+strings.Repeat("l", 8)+"fh"+strings.Repeat("l", 8)+"f" {
		t.Fatalf("rows %s", got)
	}
	if rows[0].N != 1 || rows[10].Line != 9 || rows[10].N != 8 || rows[len(rows)-1].N != 7 {
		t.Errorf("folds %+v %+v %+v", rows[0], rows[10], rows[len(rows)-1])
	}
	open := Rows(ls, 3, func(first int) bool { return first == 9 })
	if got := kinds(open); got != "fh"+strings.Repeat("l", 8)+strings.Repeat("l", 8)+"h"+strings.Repeat("l", 8)+"f" {
		t.Errorf("opened rows %s", got)
	}
}

// TestCommentRef maps a comment's lines to file:line references: new
// numbers when any line has one, old numbers for deleted lines alone.
func TestCommentRef(t *testing.T) {
	ls := lines(10, 4, 5)
	for _, c := range []struct {
		from, to int
		want     string
	}{
		{0, 0, "a.go:1"},
		{2, 6, "a.go:3-5"}, // 3, -4, +4, -5, +5
		{3, 3, "a.go, deleted line 4"},
		{3, 5, "a.go:4"}, // -4, +4, -5
	} {
		got := Comment{Path: "a.go", Lines: ls[c.from : c.to+1]}.Ref()
		if got != c.want {
			t.Errorf("lines %d-%d: %q, want %q", c.from, c.to, got, c.want)
		}
	}
	del := []Line{{'-', 7, 0, "x"}, {'-', 8, 0, "y"}}
	if got := (Comment{Path: "b", Lines: del}).Ref(); got != "b, deleted lines 7-8" {
		t.Errorf("deleted range %q", got)
	}
}

func TestPrompt(t *testing.T) {
	cs := []Comment{
		{Path: "a.go", Lines: []Line{{'-', 3, 0, "x := 1"}, {'+', 0, 3, "x := 2"}}, Text: " Why 2? \n"},
		{Path: "b.go", Lines: []Line{{' ', 9, 10, "return nil"}}, Text: "Return the error."},
	}
	want := `Review comments on your changes. Each names a file and line, quotes the diff, then says what to change.

a.go:3
> -x := 1
> +x := 2
Why 2?

b.go:10
>  return nil
Return the error.`
	if got := Prompt(cs); got != want {
		t.Errorf("Prompt:\n%s\nwant:\n%s", got, want)
	}
	long := Comment{Path: "c", Text: "t"}
	for i := range 20 {
		long.Lines = append(long.Lines, Line{'+', 0, i + 1, "l"})
	}
	if got := Prompt([]Comment{long}); !strings.Contains(got, "> ... 8 more lines\nt") || strings.Count(got, "> +l") != maxQuote {
		t.Errorf("long comment:\n%s", got)
	}
}
