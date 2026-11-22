package config

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// GhosttyThemesDirEnv, when set, is checked first when looking up Ghostty
// themes by name, for tests and custom installs.
const GhosttyThemesDirEnv = "PITWALL_GHOSTTY_THEMES_DIR"

// TerminalScheme is a terminal color scheme parsed from a Ghostty theme file
// or an iTerm2 .itermcolors plist, before mapping to pitwall's UI tokens.
type TerminalScheme struct {
	Background          rgb8
	HasBackground       bool
	Foreground          rgb8
	HasForeground       bool
	Cursor              rgb8
	HasCursor           bool
	SelectionBackground rgb8
	HasSelectionBg      bool
	SelectionForeground rgb8
	HasSelectionFg      bool
	ANSI                [16]rgb8
	HasANSI             [16]bool
}

// rgba8 is an 8-bit color with alpha, used during import before flattening.
type rgba8 struct {
	rgb rgb8
	a   uint8
}

func (c rgba8) flattenOver(bg rgb8) rgb8 {
	if c.a == 0xff {
		return c.rgb
	}
	return bg.mix(c.rgb, float64(c.a)/255)
}

// ParseTerminalTheme parses data as an iTerm2 .itermcolors plist or a Ghostty
// theme file, choosing the format from filename and content.
func ParseTerminalTheme(filename string, data []byte) (TerminalScheme, error) {
	trimmed := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	trimmed = bytes.TrimSpace(trimmed)
	if strings.EqualFold(filepath.Ext(filename), ".itermcolors") ||
		bytes.HasPrefix(trimmed, []byte("<?xml")) ||
		bytes.HasPrefix(trimmed, []byte("<plist")) ||
		bytes.HasPrefix(trimmed, []byte("<!DOCTYPE plist")) {
		return ParseITermColors(data)
	}
	return ParseGhosttyTheme(data)
}

// ParseGhosttyTheme parses a Ghostty theme file (key = value lines:
// background, foreground, palette = N=#rrggbb, cursor-color,
// selection-background, selection-foreground).
func ParseGhosttyTheme(data []byte) (TerminalScheme, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var (
		bg, fg, cur, selBg, selFg rgba8
		hasBg, hasFg, hasCur      bool
		hasSelBg, hasSelFg        bool
		ansiRaw                   [16]rgba8
		hasANSI                   [16]bool
		anyKey                    bool
	)
	for lineNo, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "palette" {
			idxStr, colStr, ok := strings.Cut(v, "=")
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid palette entry %q", lineNo+1, strings.TrimSpace(v))
			}
			idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil || idx < 0 || idx > 255 {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid palette index %q", lineNo+1, strings.TrimSpace(idxStr))
			}
			colStr = stripGhosttyValue(colStr)
			c, ok := parseImportHex(colStr)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid palette color %q", lineNo+1, colStr)
			}
			anyKey = true
			if idx < 16 {
				ansiRaw[idx], hasANSI[idx] = c, true
			}
			continue
		}
		v = stripGhosttyValue(v)
		switch k {
		case "background":
			c, ok := parseImportHex(v)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid background color %q", lineNo+1, v)
			}
			bg, hasBg, anyKey = c, true, true
		case "foreground":
			c, ok := parseImportHex(v)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid foreground color %q", lineNo+1, v)
			}
			fg, hasFg, anyKey = c, true, true
		case "cursor-color":
			if isGhosttyKeyword(v) {
				continue
			}
			c, ok := parseImportHex(v)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid cursor-color %q", lineNo+1, v)
			}
			cur, hasCur, anyKey = c, true, true
		case "selection-background":
			if isGhosttyKeyword(v) {
				continue
			}
			c, ok := parseImportHex(v)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid selection-background %q", lineNo+1, v)
			}
			selBg, hasSelBg, anyKey = c, true, true
		case "selection-foreground":
			if isGhosttyKeyword(v) {
				continue
			}
			c, ok := parseImportHex(v)
			if !ok {
				return TerminalScheme{}, fmt.Errorf("line %d: invalid selection-foreground %q", lineNo+1, v)
			}
			selFg, hasSelFg, anyKey = c, true, true
		}
	}
	if !anyKey {
		return TerminalScheme{}, errors.New("no Ghostty theme colors found")
	}
	return finalizeScheme(bg, hasBg, fg, hasFg, cur, hasCur, selBg, hasSelBg, selFg, hasSelFg, ansiRaw, hasANSI)
}

func isGhosttyKeyword(v string) bool {
	switch strings.ToLower(v) {
	case "cell-foreground", "cell-background", "none":
		return true
	}
	return false
}

// stripGhosttyValue trims whitespace, removes matching quotes, and strips an
// inline comment preceded by whitespace without eating a leading #rrggbb color.
func stripGhosttyValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return strings.TrimSpace(v[1 : len(v)-1])
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = strings.TrimSpace(v[:i])
			break
		}
	}
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	return v
}

// parseImportHex accepts #rgb, #rrggbb, #rrggbbaa, or bare hex without '#'.
func parseImportHex(s string) (rgba8, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2], 'f', 'f'})
	case 6:
		s += "ff"
	case 8:
	default:
		return rgba8{}, false
	}
	var out [4]uint8
	for i := range 4 {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return rgba8{}, false
		}
		out[i] = uint8(v)
	}
	return rgba8{rgb: rgb8{out[0], out[1], out[2]}, a: out[3]}, true
}

// ParseITermColors parses an Apple XML plist .itermcolors file.
func ParseITermColors(data []byte) (TerminalScheme, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rootDict map[string]map[string]float64
	foundPlist := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return TerminalScheme{}, fmt.Errorf("parse plist: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "plist":
			foundPlist = true
		case "dict":
			d, err := parsePlistRootDict(dec)
			if err != nil {
				return TerminalScheme{}, err
			}
			rootDict = d
			foundPlist = true
		}
		if rootDict != nil {
			break
		}
	}
	if !foundPlist || len(rootDict) == 0 {
		return TerminalScheme{}, errors.New("no iTerm2 color entries found")
	}
	var (
		bg, fg, cur, selBg, selFg rgba8
		hasBg, hasFg, hasCur      bool
		hasSelBg, hasSelFg        bool
		ansiRaw                   [16]rgba8
		hasANSI                   [16]bool
		anyColor                  bool
	)
	for name, comps := range rootDict {
		c, ok := itermDictToRGBA(comps)
		if !ok {
			continue
		}
		switch name {
		case "Background Color":
			bg, hasBg, anyColor = c, true, true
		case "Foreground Color":
			fg, hasFg, anyColor = c, true, true
		case "Cursor Color":
			cur, hasCur, anyColor = c, true, true
		case "Selection Color":
			selBg, hasSelBg, anyColor = c, true, true
		case "Selected Text Color":
			selFg, hasSelFg, anyColor = c, true, true
		default:
			if rest, ok := strings.CutPrefix(name, "Ansi "); ok {
				if numStr, ok := strings.CutSuffix(rest, " Color"); ok {
					if idx, err := strconv.Atoi(numStr); err == nil && idx >= 0 && idx < 16 {
						ansiRaw[idx], hasANSI[idx], anyColor = c, true, true
					}
				}
			}
		}
	}
	if !anyColor {
		return TerminalScheme{}, errors.New("no iTerm2 color entries found")
	}
	return finalizeScheme(bg, hasBg, fg, hasFg, cur, hasCur, selBg, hasSelBg, selFg, hasSelFg, ansiRaw, hasANSI)
}

func parsePlistRootDict(dec *xml.Decoder) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	var curKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse plist dict: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return out, nil
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, fmt.Errorf("parse plist key: %w", err)
				}
				curKey = strings.TrimSpace(k)
			case "dict":
				sub, err := parsePlistColorDict(dec)
				if err != nil {
					return nil, err
				}
				if curKey != "" {
					out[curKey] = sub
					curKey = ""
				}
			default:
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("parse plist skip <%s>: %w", t.Name.Local, err)
				}
				curKey = ""
			}
		}
	}
}

func parsePlistColorDict(dec *xml.Decoder) (map[string]float64, error) {
	comps := map[string]float64{}
	var curKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse plist color dict: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return comps, nil
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, fmt.Errorf("parse plist color key: %w", err)
				}
				curKey = strings.TrimSpace(k)
			case "real", "integer":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return nil, fmt.Errorf("parse plist number: %w", err)
				}
				if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && curKey != "" {
					comps[curKey] = v
				}
				curKey = ""
			default:
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("parse plist skip <%s>: %w", t.Name.Local, err)
				}
				curKey = ""
			}
		}
	}
}

func itermDictToRGBA(comps map[string]float64) (rgba8, bool) {
	r, okR := comps["Red Component"]
	g, okG := comps["Green Component"]
	b, okB := comps["Blue Component"]
	if !okR || !okG || !okB {
		return rgba8{}, false
	}
	a := 1.0
	if v, ok := comps["Alpha Component"]; ok {
		a = v
	}
	ch := func(v float64) uint8 {
		if math.IsNaN(v) || v <= 0 {
			return 0
		}
		if v >= 1 {
			return 255
		}
		return uint8(math.Round(v * 255))
	}
	return rgba8{rgb: rgb8{ch(r), ch(g), ch(b)}, a: ch(a)}, true
}

// finalizeScheme fills missing fields with sensible fallbacks and flattens
// translucent colors over the background so every color is opaque #rrggbb.
func finalizeScheme(
	bg rgba8, hasBg bool,
	fg rgba8, hasFg bool,
	cur rgba8, hasCur bool,
	selBg rgba8, hasSelBg bool,
	selFg rgba8, hasSelFg bool,
	ansiRaw [16]rgba8, hasANSI [16]bool,
) (TerminalScheme, error) {
	var s TerminalScheme
	if !hasBg {
		if hasANSI[0] {
			bg, hasBg = ansiRaw[0], true
		} else {
			return TerminalScheme{}, errors.New("theme specifies no background color")
		}
	}
	// Keep the background's RGB channels even if alpha < 255 (transparent
	// terminal backgrounds should not become solid black).
	s.Background, s.HasBackground = bg.rgb, true

	if hasFg {
		s.Foreground, s.HasForeground = fg.flattenOver(s.Background), true
	} else if hasANSI[7] {
		s.Foreground, s.HasForeground = ansiRaw[7].flattenOver(s.Background), true
	} else if s.Background.luminance() > 0.4 {
		s.Foreground, s.HasForeground = rgb8{0x1d, 0x1d, 0x1f}, true
	} else {
		s.Foreground, s.HasForeground = rgb8{0xf2, 0xf3, 0xf5}, true
	}

	if hasCur {
		s.Cursor, s.HasCursor = cur.flattenOver(s.Background), true
	} else {
		s.Cursor, s.HasCursor = s.Foreground, true
	}

	if hasSelBg {
		s.SelectionBackground, s.HasSelectionBg = selBg.flattenOver(s.Background), true
	}
	if hasSelFg {
		base := s.Background
		if s.HasSelectionBg {
			base = s.SelectionBackground
		}
		s.SelectionForeground, s.HasSelectionFg = selFg.flattenOver(base), true
	}

	light := s.Background.luminance() > s.Foreground.luminance()
	fallback := builtins["aide-dark"].Terminal.ANSI
	if light {
		fallback = builtins["aide-light"].Terminal.ANSI
	}
	for i := range 16 {
		if hasANSI[i] {
			s.ANSI[i], s.HasANSI[i] = ansiRaw[i].flattenOver(s.Background), true
		} else if i >= 8 && hasANSI[i-8] {
			// Bright slot falls back to its normal counterpart when missing.
			s.ANSI[i] = ansiRaw[i-8].flattenOver(s.Background)
		} else if i < len(fallback) {
			if c, ok := omarchyHex(string(fallback[i])); ok {
				s.ANSI[i] = c
			}
		}
	}
	return s, nil
}

// luminance is the WCAG 2.1 relative luminance of c, 0 to 1.
func (c rgb8) luminance() float64 {
	var l float64
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		s := float64(c[i]) / 255
		if s <= 0.04045 {
			s /= 12.92
		} else {
			s = math.Pow((s+0.055)/1.055, 2.4)
		}
		l += w * s
	}
	return l
}

// readableAt moves c toward end in fine steps until it reaches floor on every
// ground; end itself when no step does.
func readableAt(c, end rgb8, floor float64, grounds ...rgb8) rgb8 {
	const steps = 40
	for i := 0; i <= steps; i++ {
		m := c.mix(end, float64(i)/steps)
		ok := true
		for _, g := range grounds {
			if contrast(m, g) < floor {
				ok = false
				break
			}
		}
		if ok {
			return m
		}
	}
	return end
}

// readableAccent moves c toward end until it reaches markFloor on all grounds
// AND at least one candidate in onChoices reaches textFloor against it.
func readableAccent(c, end rgb8, grounds []rgb8, onChoices ...rgb8) (accent, on rgb8) {
	bestOn := func(a rgb8) (rgb8, float64) {
		best, bestR := onChoices[0], contrast(onChoices[0], a)
		for _, cand := range onChoices[1:] {
			if r := contrast(cand, a); r > bestR {
				best, bestR = cand, r
			}
		}
		return best, bestR
	}
	const steps = 40
	for i := 0; i <= steps; i++ {
		m := c.mix(end, float64(i)/steps)
		ok := true
		for _, g := range grounds {
			if contrast(m, g) < markFloor {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if o, r := bestOn(m); r >= textFloor {
			return m, o
		}
	}
	o, _ := bestOn(end)
	return end, o
}

// MapTerminalScheme maps a terminal color scheme into a full pitwall Theme:
// terminal colors directly, and UI tokens (surfaces, borders, muted text,
// primary, status colors) derived with the same WCAG contrast floors the
// built-in themes follow (4.5:1 for text, on_primary, on_danger and status
// chips; 3:1 for accent and status marks).
func MapTerminalScheme(s TerminalScheme) Theme {
	bg, fg := s.Background, s.Foreground
	light := bg.luminance() > fg.luminance()

	black := rgb8{0x00, 0x00, 0x00}
	white := rgb8{0xff, 0xff, 0xff}
	end := white
	if light {
		end = black
	}

	var side, surface, surf2, elev rgb8
	if light {
		side = bg.mix(fg, 0.04)
		surface = bg
		surf2 = bg.mix(fg, 0.07)
		elev = bg.mix(fg, 0.11)
	} else if bg.luminance() < 0.015 {
		// Near-black backgrounds (like aide-dark) keep sidebar on bg and lift
		// the card/dialog surfaces subtly so layers remain distinct.
		side = bg
		surface = bg.mix(fg, 0.05)
		surf2 = bg.mix(fg, 0.09)
		elev = bg.mix(fg, 0.14)
	} else {
		side = bg.mix(black, 0.25)
		surface = bg
		surf2 = bg.mix(fg, 0.07)
		elev = bg.mix(fg, 0.13)
	}
	border := side.mix(fg, 0.10)

	grounds := []rgb8{side, surface, surf2}
	textGrounds := []rgb8{side, surface, surf2, elev}

	fgUI := readableAt(fg, end, textFloor, textGrounds...)

	// Muted starts from ANSI 8 (bright black / comment color) when it sits
	// between bg and fg, otherwise a 58% blend from surface toward fgUI.
	mutedBase := surface.mix(fgUI, 0.58)
	if s.HasANSI[8] && contrast(s.ANSI[8], surface) >= 1.8 && contrast(s.ANSI[8], fgUI) >= 1.2 {
		mutedBase = s.ANSI[8]
	}
	muted := readableAt(mutedBase, fgUI, textFloor, textGrounds...)
	for _, g := range textGrounds {
		if contrast(muted, g) < textFloor {
			muted = readableAt(muted, end, textFloor, textGrounds...)
			break
		}
	}

	// Pick blue/primary from ANSI 4, preferring ANSI 12 if ANSI 4 is too dim.
	blueBase := s.ANSI[4]
	if contrast(s.ANSI[12], surface) > contrast(blueBase, surface)+1.0 && contrast(blueBase, surface) < markFloor {
		blueBase = s.ANSI[12]
	}
	// Primary must read as a mark on all surfaces AND hold readable OnPrimary text.
	onPrimaryChoices := []rgb8{side, white, black}
	if light {
		onPrimaryChoices = []rgb8{white, side, black}
	}
	primary, onPrimary := readableAccent(blueBase, end, grounds, onPrimaryChoices...)

	// Red must read as a mark on all surfaces AND hold readable OnDanger text
	// (theme.derive picks between white and Bg, which is side).
	red, _ := readableAccent(s.ANSI[1], end, grounds, white, side)

	mark := func(c rgb8) rgb8 { return readableAt(c, end, markFloor, grounds...) }
	yellow := mark(s.ANSI[3])
	green := mark(s.ANSI[2])
	blue := mark(blueBase)
	purple := mark(s.ANSI[5])

	// Ensure fgUI also achieves 4.5:1 on every status chip fill (14% accent
	// over each surface), so theme.Chip always meets WCAG AA.
	var chipGrounds []rgb8
	for _, acc := range []rgb8{red, yellow, green, blue, purple} {
		for _, g := range grounds {
			chipGrounds = append(chipGrounds, g.mix(acc, 0.14))
		}
	}
	fgUI = readableAt(fgUI, end, textFloor, chipGrounds...)

	t := Theme{
		Colors: Colors{
			Bg:               side.hex(),
			Sidebar:          side.hex(),
			Surface:          surface.hex(),
			SurfaceSecondary: surf2.hex(),
			SurfaceElevated:  elev.hex(),
			Border:           border.hex(),
			Fg:               fgUI.hex(),
			Muted:            muted.hex(),
			Primary:          primary.hex(),
			OnPrimary:        onPrimary.hex(),
			Red:              red.hex(),
			Yellow:           yellow.hex(),
			Green:            green.hex(),
			Blue:             blue.hex(),
			Purple:           purple.hex(),
		},
		Terminal: Terminal{
			Foreground: s.Foreground.hex(),
			Background: s.Background.hex(),
			Cursor:     s.Cursor.hex(),
			ANSI:       make([]Color, 16),
		},
	}
	if s.HasSelectionBg {
		t.Terminal.SelectionBackground = s.SelectionBackground.hex()
	}
	if s.HasSelectionFg {
		t.Terminal.SelectionForeground = s.SelectionForeground.hex()
	}
	for i := range 16 {
		t.Terminal.ANSI[i] = s.ANSI[i].hex()
	}
	return t
}

// FormatThemeTOML serializes t as a pitwall themes/<name>.toml file.
func FormatThemeTOML(t Theme, source string) string {
	var b strings.Builder
	if source != "" {
		fmt.Fprintf(&b, "# Imported from %s\n\n", strings.ReplaceAll(source, "\n", " "))
	}
	k := t.Colors
	fmt.Fprintf(&b, "[colors]\n")
	fmt.Fprintf(&b, "bg = %s\n", Quote(string(k.Bg)))
	fmt.Fprintf(&b, "sidebar = %s\n", Quote(string(k.Sidebar)))
	fmt.Fprintf(&b, "surface = %s\n", Quote(string(k.Surface)))
	fmt.Fprintf(&b, "surface_secondary = %s\n", Quote(string(k.SurfaceSecondary)))
	fmt.Fprintf(&b, "surface_elevated = %s\n", Quote(string(k.SurfaceElevated)))
	fmt.Fprintf(&b, "border = %s\n", Quote(string(k.Border)))
	fmt.Fprintf(&b, "fg = %s\n", Quote(string(k.Fg)))
	fmt.Fprintf(&b, "muted = %s\n", Quote(string(k.Muted)))
	fmt.Fprintf(&b, "primary = %s\n", Quote(string(k.Primary)))
	fmt.Fprintf(&b, "on_primary = %s\n", Quote(string(k.OnPrimary)))
	fmt.Fprintf(&b, "red = %s\n", Quote(string(k.Red)))
	fmt.Fprintf(&b, "yellow = %s\n", Quote(string(k.Yellow)))
	fmt.Fprintf(&b, "green = %s\n", Quote(string(k.Green)))
	fmt.Fprintf(&b, "blue = %s\n", Quote(string(k.Blue)))
	fmt.Fprintf(&b, "purple = %s\n", Quote(string(k.Purple)))

	tm := t.Terminal
	fmt.Fprintf(&b, "\n[terminal]\n")
	fmt.Fprintf(&b, "foreground = %s\n", Quote(string(tm.Foreground)))
	fmt.Fprintf(&b, "background = %s\n", Quote(string(tm.Background)))
	fmt.Fprintf(&b, "cursor = %s\n", Quote(string(tm.Cursor)))
	if tm.SelectionBackground != "" {
		fmt.Fprintf(&b, "selection_background = %s\n", Quote(string(tm.SelectionBackground)))
	}
	if tm.SelectionForeground != "" {
		fmt.Fprintf(&b, "selection_foreground = %s\n", Quote(string(tm.SelectionForeground)))
	}
	b.WriteString("ansi = [\n")
	for row := range 2 {
		b.WriteString("  ")
		for col := range 8 {
			i := row*8 + col
			if col > 0 {
				b.WriteString(", ")
			}
			var c Color
			if i < len(tm.ANSI) {
				c = tm.ANSI[i]
			}
			b.WriteString(Quote(string(c)))
		}
		if row == 0 {
			b.WriteString(",\n")
		} else {
			b.WriteString(",\n]\n")
		}
	}
	return b.String()
}

// GhosttyThemeDirs returns the directories checked when importing a Ghostty
// theme by name, highest priority first.
func GhosttyThemeDirs() []string {
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if env := os.Getenv(GhosttyThemesDirEnv); env != "" {
		for _, p := range filepath.SplitList(env) {
			add(p)
		}
		return dirs
	}
	if d := os.Getenv("GHOSTTY_RESOURCES_DIR"); d != "" {
		add(filepath.Join(d, "themes"))
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		add(filepath.Join(xdg, "ghostty", "themes"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		add(filepath.Join(home, ".config", "ghostty", "themes"))
		if runtime.GOOS == "darwin" {
			add(filepath.Join(home, "Library", "Application Support", "com.mitchellh.ghostty", "themes"))
		}
	}
	if xdgData := os.Getenv("XDG_DATA_DIRS"); xdgData != "" {
		for _, d := range filepath.SplitList(xdgData) {
			if d != "" {
				add(filepath.Join(d, "ghostty", "themes"))
			}
		}
	}
	add("/usr/share/ghostty/themes")
	add("/usr/local/share/ghostty/themes")
	add("/opt/homebrew/share/ghostty/themes")
	add("/Applications/Ghostty.app/Contents/Resources/ghostty/themes")
	return dirs
}

// ListGhosttyThemes returns the sorted unique theme names installed in
// Ghostty's theme directories.
func ListGhosttyThemes() []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range GhosttyThemeDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e.Name())
			}
		}
	}
	slices.Sort(out)
	return out
}

// FindGhosttyTheme resolves a Ghostty theme name in GhosttyThemeDirs by exact
// match, case-insensitive match, or slug match.
func FindGhosttyTheme(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid Ghostty theme name %q", name)
	}
	wantSlug := ThemeSlug(name)
	var ciMatch, slugMatch string
	for _, dir := range GhosttyThemeDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			en := e.Name()
			if en == name {
				p := filepath.Join(dir, en)
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					return p, nil
				}
			}
			if ciMatch == "" && strings.EqualFold(en, name) {
				p := filepath.Join(dir, en)
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					ciMatch = p
				}
			}
			if slugMatch == "" && wantSlug != "" && ThemeSlug(en) == wantSlug {
				p := filepath.Join(dir, en)
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					slugMatch = p
				}
			}
		}
	}
	if ciMatch != "" {
		return ciMatch, nil
	}
	if slugMatch != "" {
		return slugMatch, nil
	}
	return "", fmt.Errorf("no Ghostty theme named %q found", name)
}

// ThemeSlug turns a filename or display name into a safe pitwall theme name
// (lowercase letters, digits and hyphens).
func ThemeSlug(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	for _, ext := range []string{".itermcolors", ".ghostty", ".toml", ".conf", ".txt"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ImportedTheme is one theme saved by ImportTheme.
type ImportedTheme struct {
	Name   string
	Path   string
	Source string
	Theme  Theme
}

// ImportTheme imports a Ghostty theme file, an iTerm2 .itermcolors file, a
// directory of theme files, or a Ghostty theme by name, writing the mapped
// pitwall .toml file(s) into themesDir.
func ImportTheme(arg, customName, themesDir string) ([]ImportedTheme, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, errors.New("theme source is empty")
	}
	if strings.HasPrefix(arg, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			arg = filepath.Join(home, arg[2:])
		}
	}
	fi, statErr := os.Stat(arg)
	if statErr == nil && fi.IsDir() {
		if customName != "" {
			return nil, errors.New("--name cannot be used when importing a directory")
		}
		return importThemeDir(arg, themesDir)
	}
	sourcePath := arg
	if statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		found, err := FindGhosttyTheme(arg)
		if err != nil {
			return nil, fmt.Errorf("%q is not a file, directory, or installed Ghostty theme", arg)
		}
		sourcePath = found
	}
	res, err := importThemeFile(sourcePath, customName, themesDir)
	if err != nil {
		return nil, err
	}
	return []ImportedTheme{res}, nil
}

func importThemeDir(dir, themesDir string) ([]ImportedTheme, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ImportedTheme
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if skipDirEntry(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		res, err := importThemeFile(path, "", themesDir)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", e.Name(), err)
			}
			continue
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		if firstErr != nil {
			return nil, fmt.Errorf("no valid themes in %s (%v)", dir, firstErr)
		}
		return nil, fmt.Errorf("no theme files found in %s", dir)
	}
	return out, nil
}

func skipDirEntry(name string) bool {
	low := strings.ToLower(name)
	switch low {
	case "readme", "readme.md", "license", "license.md", "license.txt", "copying", ".ds_store":
		return true
	}
	switch filepath.Ext(low) {
	case ".md", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".json", ".yaml", ".yml", ".sh":
		return true
	}
	return false
}

func importThemeFile(path, customName, themesDir string) (ImportedTheme, error) {
	data, err := readCapped(path, 1<<20)
	if err != nil {
		return ImportedTheme{}, err
	}
	scheme, err := ParseTerminalTheme(path, data)
	if err != nil {
		return ImportedTheme{}, err
	}
	th := MapTerminalScheme(scheme)
	rawName := customName
	if rawName == "" {
		rawName = filepath.Base(path)
	}
	slug := ThemeSlug(rawName)
	if slug == "" {
		return ImportedTheme{}, fmt.Errorf("cannot derive a theme name from %q", rawName)
	}
	if customName == "" && isReservedThemeName(slug) {
		slug += "-imported"
	} else if isReservedThemeName(slug) {
		return ImportedTheme{}, fmt.Errorf("theme name %q is reserved or built in; choose another name", slug)
	}
	dst := filepath.Join(themesDir, slug+".toml")
	content := FormatThemeTOML(th, filepath.Base(path))
	if err := writeFile(dst, []byte(content), 0o644); err != nil {
		return ImportedTheme{}, err
	}
	return ImportedTheme{Name: slug, Path: dst, Source: path, Theme: th}, nil
}

func isReservedThemeName(name string) bool {
	if name == OmarchyName || name == SystemThemeName {
		return true
	}
	_, ok := builtins[name]
	return ok
}
