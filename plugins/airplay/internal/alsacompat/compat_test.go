package alsacompat

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fixtureSymbol struct {
	name    string
	version uint16
	typ     elf.SymType
}

// Build an inert ARM32 ELF fixture: no proprietary firmware is needed in CI.
func fixture(symbols ...fixtureSymbol) []byte {
	order := binary.LittleEndian
	sectionNames := []string{"", ".shstrtab", ".dynstr", ".dynsym", ".gnu.version", ".text"}
	shstr := []byte{0}
	nameOffsets := make([]uint32, len(sectionNames))
	for i, name := range sectionNames[1:] {
		nameOffsets[i+1] = uint32(len(shstr))
		shstr = append(shstr, append([]byte(name), 0)...)
	}
	strtab := []byte{0}
	symtab := make([]byte, (len(symbols)+1)*16)
	versions := make([]byte, (len(symbols)+1)*2)
	for i, sym := range symbols {
		at := (i + 1) * 16
		order.PutUint32(symtab[at:], uint32(len(strtab)))
		strtab = append(strtab, append([]byte(sym.name), 0)...)
		order.PutUint32(symtab[at+4:], uint32(0x1000+i*4))
		order.PutUint32(symtab[at+8:], 2)
		symtab[at+12] = byte(elf.STB_GLOBAL)<<4 | byte(sym.typ)
		order.PutUint16(symtab[at+14:], 5)
		order.PutUint16(versions[(i+1)*2:], sym.version)
	}
	data := make([]byte, 52+40*len(sectionNames))
	copy(data, []byte("\x7fELF\x01\x01\x01"))
	order.PutUint16(data[16:], uint16(elf.ET_DYN))
	order.PutUint16(data[18:], uint16(elf.EM_ARM))
	order.PutUint32(data[20:], 1)
	order.PutUint32(data[32:], 52)
	order.PutUint16(data[40:], 52)
	order.PutUint16(data[46:], 40)
	order.PutUint16(data[48:], uint16(len(sectionNames)))
	order.PutUint16(data[50:], 1)
	sections := [][]byte{nil, shstr, strtab, symtab, versions, {0x70, 0x47}}
	types := []elf.SectionType{elf.SHT_NULL, elf.SHT_STRTAB, elf.SHT_STRTAB, elf.SHT_DYNSYM, elf.SHT_GNU_VERSYM, elf.SHT_PROGBITS}
	for i := 1; i < len(sections); i++ {
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		header := data[52+i*40:]
		order.PutUint32(header, nameOffsets[i])
		order.PutUint32(header[4:], uint32(types[i]))
		order.PutUint32(header[16:], uint32(len(data)))
		order.PutUint32(header[20:], uint32(len(sections[i])))
		order.PutUint32(header[32:], 4)
		if i == 3 {
			order.PutUint32(header[24:], 2)
			order.PutUint32(header[36:], 16)
		}
		if i == 4 {
			order.PutUint32(header[24:], 3)
			order.PutUint32(header[36:], 2)
		}
		data = append(data, sections[i]...)
	}
	return data
}

func section(t *testing.T, data []byte, name string) *elf.Section {
	t.Helper()
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s := f.Section(name)
	if s == nil {
		t.Fatalf("missing fixture section %s", name)
	}
	return s
}

func TestModernExportsKeepsCurrentAPIAndExecutableCode(t *testing.T) {
	data := fixture(
		fixtureSymbol{"snd_pcm_hw_params_get_period_size", 0x8002, elf.STT_FUNC},
		fixtureSymbol{"snd_pcm_hw_params_get_period_size", 3, elf.STT_FUNC},
		fixtureSymbol{"snd_pcm_hw_params_get_buffer_size", 0x8002, elf.STT_FUNC},
		fixtureSymbol{"snd_pcm_hw_params_get_buffer_size", 3, elf.STT_FUNC},
		fixtureSymbol{"snd_pcm_data", 0x8002, elf.STT_OBJECT},
		fixtureSymbol{"snd_pcm_data", 3, elf.STT_OBJECT},
		fixtureSymbol{"other_function", 0x8002, elf.STT_FUNC},
		fixtureSymbol{"other_function", 3, elf.STT_FUNC},
	)
	before := append([]byte(nil), data...)
	patched, count, err := modernALSAExports(data)
	if err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if !bytes.Equal(data, before) || len(patched) != len(before) {
		t.Fatal("input or ELF layout changed")
	}
	// Only the two obsolete st_name fields may change; all code, current
	// symbols, object symbols, and unrelated exports must remain identical.
	expected := append([]byte(nil), before...)
	s := section(t, data, ".dynsym")
	for _, index := range []uint64{1, 3} {
		binary.LittleEndian.PutUint32(expected[s.Offset+index*16:], 0)
	}
	if !bytes.Equal(patched, expected) {
		t.Fatal("modified more than the obsolete symbol names")
	}
	f, err := elf.NewFile(bytes.NewReader(patched))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	exports, err := f.DynamicSymbols()
	if err != nil {
		t.Fatal(err)
	}
	if exports[0].Name != "" || exports[1].Name != "snd_pcm_hw_params_get_period_size" || exports[2].Name != "" || exports[3].Name != "snd_pcm_hw_params_get_buffer_size" {
		t.Fatal("current exports were not preserved")
	}
}

func TestModernExportsLeavesOtherVersionsAlone(t *testing.T) {
	data := fixture(fixtureSymbol{"snd_pcm_get_something", 0x8002, elf.STT_FUNC}, fixtureSymbol{"snd_pcm_get_something", 4, elf.STT_FUNC})
	patched, count, err := modernALSAExports(data)
	if err != nil || count != 0 || !bytes.Equal(data, patched) {
		t.Fatal("changed an unsupported version pair", count, err)
	}
}

func TestModernExportsRejectsMalformedMetadata(t *testing.T) {
	valid := fixture(fixtureSymbol{"snd_pcm_get_something", 0x8002, elf.STT_FUNC}, fixtureSymbol{"snd_pcm_get_something", 3, elf.STT_FUNC})
	for _, kind := range []string{"truncated ELF", "invalid name", "unterminated name", "short version table", "invalid symbol entry size", "truncated symbols"} {
		t.Run(kind, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			switch kind {
			case "truncated ELF":
				data = data[:24]
			case "invalid name":
				s := section(t, data, ".dynsym")
				binary.LittleEndian.PutUint32(data[s.Offset+16:], 0xffffffff)
			case "unterminated name":
				s := section(t, data, ".dynstr")
				data[s.Offset+s.Size-1] = 'x'
			case "short version table":
				binary.LittleEndian.PutUint32(data[52+4*40+20:], 2)
			case "invalid symbol entry size":
				binary.LittleEndian.PutUint32(data[52+3*40+36:], 15)
			case "truncated symbols":
				binary.LittleEndian.PutUint32(data[52+3*40+16:], uint32(len(data)+16))
			}
			if _, _, err := modernALSAExports(data); err == nil {
				t.Fatal("accepted malformed metadata")
			}
		})
	}
}

func TestPrepareDoesNotChangeOtherFirmware(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "firmware-library")
	player := filepath.Join(root, "firmware-player")
	for _, path := range []string{library, player} {
		if err := os.WriteFile(path, []byte("other firmware"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := Prepare(root, library, player)
	if err != nil || dir != "" {
		t.Fatal("workaround applied to another firmware", dir, err)
	}
	if _, err := os.Stat(filepath.Join(root, "alsa-compat")); !os.IsNotExist(err) {
		t.Fatal("created a compatibility directory for other firmware")
	}
	for _, path := range []string{library, player} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "other firmware" {
			t.Fatal("modified firmware", err)
		}
	}
}

func TestVerifiedFirmwareRegression(t *testing.T) {
	// Optional private fixtures, never redistributed or required by CI.
	library, player := os.Getenv("AIRPLAY_TEST_ALSA"), os.Getenv("AIRPLAY_TEST_APLAY")
	if library == "" || player == "" {
		t.Skip("private firmware fixtures not supplied")
	}
	original, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir, err := Prepare(root, library, player)
	if err != nil || dir == "" {
		t.Fatal("verified firmware was not repaired", dir, err)
	}
	path := filepath.Join(dir, "libasound.so.2")
	patched, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(patched)); got != "fa6d683ab80a5dcc6edf240aa4148495d074b847f79e731a5c1599cd8ee84d44" {
		t.Fatalf("differs from the copy that produced audible sound: %s", got)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("private library mode", stat, err)
	}
	for _, name := range []string{".text", ".rodata", ".data", ".gnu.version"} {
		a, e := section(t, original, name).Data()
		if e != nil {
			t.Fatal(e)
		}
		b, e := section(t, patched, name).Data()
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("changed %s", name)
		}
	}
	if _, err := Prepare(root, library, player); err != nil {
		t.Fatal("cannot reuse compatibility copy", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary library leaked", entries, err)
	}
	unchanged, err := os.ReadFile(library)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("modified firmware", err)
	}
	otherPlayer := filepath.Join(root, "other-player")
	if err := os.WriteFile(otherPlayer, []byte("different player"), 0600); err != nil {
		t.Fatal(err)
	}
	if dir, err := Prepare(t.TempDir(), library, otherPlayer); err != nil || dir != "" {
		t.Fatal("accepted an unverified player", dir, err)
	}
}
