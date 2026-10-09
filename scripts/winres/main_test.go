package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestSyso reads each committed .syso back as Windows would: the icon
// group 1 lists every size, and each entry leads to that size's PNG
// through a relocated data entry. It also fails when the files are stale.
func TestSyso(t *testing.T) {
	icns, err := os.ReadFile("../../packaging/pitwall.icns")
	if err != nil {
		t.Fatal(err)
	}
	pngs, err := iconPNGs(icns)
	if err != nil {
		t.Fatal(err)
	}
	for arch, m := range machines {
		t.Run(arch, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("../../cmd/pitwall", "rsrc_windows_"+arch+".syso"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, syso(arch, pngs)) {
				t.Fatal("stale: run go run ./scripts/winres from the repository root")
			}
			f, err := pe.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if f.Machine != m.machine || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" {
				t.Fatalf("machine %#x, sections %v", f.Machine, f.Sections)
			}
			sect, err := f.Sections[0].Data()
			if err != nil {
				t.Fatal(err)
			}
			relocated := map[uint32]bool{}
			for _, r := range f.Sections[0].Relocs {
				if r.Type != m.reloc || r.SymbolTableIndex != 0 {
					t.Fatalf("reloc %+v", r)
				}
				relocated[r.VirtualAddress] = true
			}
			u32 := func(off uint32) uint32 { return binary.LittleEndian.Uint32(sect[off:]) }
			// lookup follows id from the directory at off; it returns the
			// next directory's offset, or for a data entry its data.
			lookup := func(off, id uint32) (uint32, []byte) {
				t.Helper()
				n := uint32(binary.LittleEndian.Uint16(sect[off+12:])) + uint32(binary.LittleEndian.Uint16(sect[off+14:]))
				for e := off + 16; e < off+16+8*n; e += 8 {
					if id != 0 && u32(e) != id {
						continue
					}
					to := u32(e + 4)
					if to&(1<<31) != 0 {
						return to &^ (1 << 31), nil
					}
					if !relocated[to] {
						t.Fatalf("data entry at %d has no relocation", to)
					}
					return 0, sect[u32(to) : u32(to)+u32(to+4)]
				}
				t.Fatalf("no entry %d in the directory at %d", id, off)
				return 0, nil
			}
			resource := func(typ, id uint32) []byte {
				names, _ := lookup(0, typ)
				langs, _ := lookup(names, id)
				_, data := lookup(langs, 0)
				return data
			}
			group := resource(rtGroupIcon, 1)
			if n := int(binary.LittleEndian.Uint16(group[4:])); n != len(sizes) || len(group) != 6+14*n {
				t.Fatalf("group of %d bytes lists %d icons", len(group), n)
			}
			for i, size := range sizes {
				e := group[6+14*i:]
				id := uint32(binary.LittleEndian.Uint16(e[12:]))
				icon := resource(rtIcon, id)
				if w, _, _ := pngSize(icon); w != size || int(e[0]) != size%256 || binary.LittleEndian.Uint32(e[8:]) != uint32(len(icon)) || !bytes.Equal(icon, pngs[i]) {
					t.Errorf("icon %d: %dpx PNG, entry %v", id, w, e[:14])
				}
			}
		})
	}
}
