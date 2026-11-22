package vt

import (
	"io"
	"strings"
	"testing"
)

func TestFormatGridTextANSIHTML(t *testing.T) {
	e := New(40, 4, io.Discard)
	e.Write([]byte("\x1b[1;31mERR\x1b[0m \x1b[38;2;18;52;86mrgb\x1b[0m \x1b[7mrev\x1b[0m\r\n"))
	e.Write([]byte("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ <tag>&\"'\r\n"))
	g := e.Snapshot()

	plain, err := FormatGrid(g, FormatText, DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ERR rgb rev\nlink <tag>&\"'"; plain != want {
		t.Fatalf("text = %q, want %q", plain, want)
	}

	ansiOut, err := FormatGrid(g, FormatANSI, DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"\x1b[0;1;31mERR\x1b[0m", "\x1b[0;38;2;18;52;86mrgb\x1b[0m", "\x1b[0;7mrev\x1b[0m"} {
		if !strings.Contains(ansiOut, sub) {
			t.Errorf("ansi output missing %q:\n%q", sub, ansiOut)
		}
	}

	pal := DefaultPalette
	pal.Bg = 0x112233
	pal.Fg = 0xaabbcc
	pal.ANSI[1] = 0xee1122
	htmlOut, err := FormatGrid(g, FormatHTML, pal)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{
		`background:#112233;color:#aabbcc`,
		`<span style="color:#ee1122;font-weight:700">ERR</span>`,
		`<span style="color:#123456">rgb</span>`,
		`<span style="color:#112233;background:#aabbcc">rev</span>`,
		`<a href="https://example.com" style="color:inherit">link</a>`,
		`&lt;tag&gt;&amp;&#34;&#39;`,
		`</pre>`,
	} {
		if !strings.Contains(htmlOut, sub) {
			t.Errorf("html output missing %q:\n%s", sub, htmlOut)
		}
	}
	if strings.Contains(htmlOut, "<tag>") {
		t.Fatalf("html output did not escape tag: %s", htmlOut)
	}
}

func TestFormatHTMLUnsafeLinkOmitted(t *testing.T) {
	e := New(40, 2, io.Discard)
	e.Write([]byte("\x1b]8;;javascript:alert(1)\x1b\\click\x1b]8;;\x1b\\"))
	out, err := FormatGrid(e.Snapshot(), FormatHTML, DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<a ") || strings.Contains(out, "javascript:") {
		t.Fatalf("unsafe link leaked into html: %s", out)
	}
	if !strings.Contains(out, "click") {
		t.Fatalf("link text missing: %s", out)
	}
}

func TestCaptureScrollbackFormatLines(t *testing.T) {
	e := New(10, 2, io.Discard)
	e.Write([]byte("\x1b[32mline1\x1b[0m\r\n\x1b[33mline2\x1b[0m\r\n\x1b[34mline3\x1b[0m\r\nline4"))
	build, err := CaptureScrollbackFormat(e, FormatANSI, 2, DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	got := build()
	if strings.Contains(got, "line1") || strings.Contains(got, "line2") {
		t.Fatalf("lines=2 kept older lines: %q", got)
	}
	if !strings.Contains(got, "\x1b[0;34mline3\x1b[0m\nline4\n") {
		t.Fatalf("scrollback ansi = %q", got)
	}

	buildHTML, err := CaptureScrollbackFormat(e, FormatHTML, 2, DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	gotHTML := buildHTML()
	if !strings.Contains(gotHTML, "line3") || !strings.Contains(gotHTML, "line4") || strings.Contains(gotHTML, "line1") {
		t.Fatalf("scrollback html = %s", gotHTML)
	}

	if _, err := FormatGrid(e.Snapshot(), "bogus", DefaultPalette); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
