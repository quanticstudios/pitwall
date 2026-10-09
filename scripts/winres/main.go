// Command winres writes the Windows icon resource that go build links into
// pitwall.exe, from the PNGs in packaging/pitwall.icns (pitwall.svg at each
// size). Run it from the repository root when the icon changes:
//
//	go run ./scripts/winres
//
// Explorer, the Start menu shortcut and the window's title bar take the
// icon from icon group 1, which Gio loads too.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
)

// sizes are the icon's sizes in pixels; Windows scales the nearest one for
// the rest.
var sizes = []int{16, 32, 64, 128, 256}

// machines are the COFF machine numbers and their ADDR32NB relocation, by
// GOARCH.
var machines = map[string]struct{ machine, reloc uint16 }{
	"amd64": {0x8664, 0x3},
	"arm64": {0xaa64, 0x2},
}

func main() {
	icns, err := os.ReadFile("packaging/pitwall.icns")
	if err != nil {
		log.Fatal(err)
	}
	pngs, err := iconPNGs(icns)
	if err != nil {
		log.Fatal(err)
	}
	for arch := range machines {
		if err := os.WriteFile("cmd/pitwall/rsrc_windows_"+arch+".syso", syso(arch, pngs), 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

// iconPNGs picks the PNG for each of sizes out of an icns file.
func iconPNGs(icns []byte) ([][]byte, error) {
	if len(icns) < 8 || string(icns[:4]) != "icns" {
		return nil, errors.New("not an icns file")
	}
	bySize := map[int][]byte{}
	for b := icns[8:]; len(b) >= 8; {
		n := int(binary.BigEndian.Uint32(b[4:8]))
		if n < 8 || n > len(b) {
			return nil, errors.New("truncated icns entry")
		}
		if w, _, ok := pngSize(b[8:n]); ok {
			bySize[w] = b[8:n]
		}
		b = b[n:]
	}
	var out [][]byte
	for _, s := range sizes {
		if bySize[s] == nil {
			return nil, fmt.Errorf("the icns has no %dpx PNG", s)
		}
		out = append(out, bySize[s])
	}
	return out, nil
}

// pngSize reads a PNG's width and height from its IHDR chunk.
func pngSize(p []byte) (w, h int, ok bool) {
	if len(p) < 24 || !bytes.HasPrefix(p, []byte("\x89PNG\r\n\x1a\n")) || string(p[12:16]) != "IHDR" {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(p[16:20])), int(binary.BigEndian.Uint32(p[20:24])), true
}

// Resource types and the language every resource is filed under.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	langEnUS    = 0x409
)

// syso is a COFF object for arch holding one .rsrc section: icon group 1
// and its icons 1 to len(pngs). Every data entry's address is relative to
// the section, with a relocation the linker resolves to the section's RVA.
func syso(arch string, pngs [][]byte) []byte {
	// The group directory: GRPICONDIR then a GRPICONDIRENTRY per icon.
	group := le(uint16(0), uint16(1), uint16(len(pngs)))
	for i, p := range pngs {
		w, h, _ := pngSize(p)
		group = append(group, byte(w), byte(h)) // 256 wraps to 0, which means 256
		group = append(group, le(uint16(0), uint16(1), uint16(32), uint32(len(p)), uint16(i+1))...)
	}

	// Directory tree: types, then names, then one language each.
	type leaf struct{ data []byte }
	types := []struct {
		id    uint32
		names []leaf
	}{{rtIcon, nil}, {rtGroupIcon, []leaf{{group}}}}
	for _, p := range pngs {
		types[0].names = append(types[0].names, leaf{p})
	}
	dir := func(n int) []byte { return le(uint32(0), uint32(0), uint16(0), uint16(0), uint16(0), uint16(n)) }
	const sub = 1 << 31 // OffsetToData points at a directory, not a data entry

	// Offsets, laid out in order: root, type directories, language
	// directories, data entries, data.
	off := 16 + 8*len(types)
	typeAt := make([]int, len(types))
	for i, t := range types {
		typeAt[i] = off
		off += 16 + 8*len(t.names)
	}
	langAt := map[[2]int]int{}
	for i, t := range types {
		for j := range t.names {
			langAt[[2]int{i, j}] = off
			off += 16 + 8
		}
	}
	entryAt := map[[2]int]int{}
	for i, t := range types {
		for j := range t.names {
			entryAt[[2]int{i, j}] = off
			off += 16
		}
	}
	dataAt := map[[2]int]int{}
	for i, t := range types {
		for j, l := range t.names {
			off = (off + 7) &^ 7
			dataAt[[2]int{i, j}] = off
			off += len(l.data)
		}
	}

	sect := dir(len(types))
	for i, t := range types {
		sect = append(sect, le(t.id, uint32(sub|typeAt[i]))...)
	}
	for i, t := range types {
		sect = append(sect, dir(len(t.names))...)
		for j := range t.names {
			sect = append(sect, le(uint32(j+1), uint32(sub|langAt[[2]int{i, j}]))...)
		}
	}
	for i, t := range types {
		for j := range t.names {
			sect = append(sect, dir(1)...)
			sect = append(sect, le(uint32(langEnUS), uint32(entryAt[[2]int{i, j}]))...)
		}
	}
	var relocs []int // offsets of the fields that hold a section-relative address
	for i, t := range types {
		for j, l := range t.names {
			relocs = append(relocs, len(sect))
			sect = append(sect, le(uint32(dataAt[[2]int{i, j}]), uint32(len(l.data)), uint32(0), uint32(0))...)
		}
	}
	for i, t := range types {
		for j, l := range t.names {
			sect = append(sect, make([]byte, dataAt[[2]int{i, j}]-len(sect))...)
			sect = append(sect, l.data...)
		}
	}

	m := machines[arch]
	const headers = 20 + 40
	relocAt := headers + len(sect)
	symAt := relocAt + 10*len(relocs)
	var obj []byte
	// IMAGE_FILE_HEADER: one section, one symbol, no optional header.
	obj = append(obj, le(m.machine, uint16(1), uint32(0), uint32(symAt), uint32(1), uint16(0), uint16(0))...)
	// IMAGE_SECTION_HEADER: initialized, read-only data.
	obj = append(obj, ".rsrc\x00\x00\x00"...)
	obj = append(obj, le(uint32(0), uint32(0), uint32(len(sect)), uint32(headers), uint32(relocAt), uint32(0), uint16(len(relocs)), uint16(0), uint32(0x40000040))...)
	obj = append(obj, sect...)
	for _, r := range relocs {
		obj = append(obj, le(uint32(r), uint32(0), m.reloc)...) // against symbol 0, the section
	}
	// The section's static symbol, then an empty string table.
	obj = append(obj, ".rsrc\x00\x00\x00"...)
	obj = append(obj, le(uint32(0), uint16(1), uint16(0), uint8(3), uint8(0))...)
	return append(obj, le(uint32(4))...)
}

// le encodes vs, fixed-size integers, little-endian.
func le(vs ...any) []byte {
	var b []byte
	for _, v := range vs {
		b, _ = binary.Append(b, binary.LittleEndian, v)
	}
	return b
}
