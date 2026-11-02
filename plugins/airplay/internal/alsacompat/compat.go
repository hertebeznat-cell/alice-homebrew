// Package alsacompat repairs one verified firmware aplay / libasound ABI pair
// without replacing any firmware files or changing Alice's ALSA library.
package alsacompat

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Restrict the repair to the pair verified on Mini 1 firmware
// 1.369.4.50.4794692641.20261021. Other firmware keeps its existing audio path.
// Its legacy ALSA_0.9 exports use return-value APIs, while the firmware aplay
// imports the modern output-pointer APIs without ELF version requirements.
const stockALSASHA256 = "df8f67691cf9a59a803f4e264ad86cd77fcb9e00da1ff4a8c1e33d2e83359f1a"
const stockAplaySHA256 = "7ffba196131d8669b04fcd16f7747bfffb1cb29b66881f04129269da47e48ee0"

// Prepare returns a private LD_LIBRARY_PATH directory for the verified pair,
// or an empty directory when this workaround does not apply. The library's
// executable code and dmix shared-memory ABI are preserved.
func Prepare(root, original, player string) (string, error) {
	data, err := os.ReadFile(original)
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != stockALSASHA256 {
		return "", nil
	}
	aplay, err := os.ReadFile(player)
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(aplay)) != stockAplaySHA256 {
		return "", nil
	}
	patched, count, err := modernALSAExports(data)
	if err != nil {
		return "", err
	}
	if count != 65 {
		return "", fmt.Errorf("unexpected ALSA compatibility table: %d aliases", count)
	}
	dir := filepath.Join(root, "alsa-compat")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "libasound.so.2")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, patched) {
		return dir, nil
	}
	temporary, err := os.CreateTemp(dir, "libasound-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(patched); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}

func modernALSAExports(data []byte) ([]byte, int, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS32 || f.Data != elf.ELFDATA2LSB || f.Machine != elf.EM_ARM {
		return nil, 0, fmt.Errorf("expected the verified ARM32 ALSA library")
	}
	symbols, names, versions := f.Section(".dynsym"), f.Section(".dynstr"), f.Section(".gnu.version")
	if symbols == nil || names == nil || versions == nil || symbols.Entsize != 16 || symbols.Size%16 != 0 {
		return nil, 0, fmt.Errorf("missing ALSA symbol-version metadata")
	}
	for _, s := range []*elf.Section{symbols, names, versions} {
		if s.Offset > uint64(len(data)) || s.Size > uint64(len(data))-s.Offset {
			return nil, 0, fmt.Errorf("truncated ALSA metadata")
		}
	}
	count := symbols.Size / symbols.Entsize
	if versions.Size < count*2 {
		return nil, 0, fmt.Errorf("truncated ALSA version table")
	}
	type entry struct {
		offset  uint64
		version uint16
	}
	groups := make(map[string][]entry)
	strtab := data[names.Offset : names.Offset+names.Size]
	for i := uint64(0); i < count; i++ {
		offset := symbols.Offset + i*16
		name := uint64(binary.LittleEndian.Uint32(data[offset:]))
		if name == 0 {
			continue
		}
		if name >= uint64(len(strtab)) {
			return nil, 0, fmt.Errorf("invalid ALSA symbol name")
		}
		end := bytes.IndexByte(strtab[name:], 0)
		if end < 0 {
			return nil, 0, fmt.Errorf("unterminated ALSA symbol name")
		}
		version := binary.LittleEndian.Uint16(data[versions.Offset+i*2:])
		nameText := string(strtab[name : name+uint64(end)])
		groups[nameText] = append(groups[nameText], entry{offset, version})
	}
	patched := append([]byte(nil), data...)
	changes := 0
	for name, entries := range groups {
		if !strings.HasPrefix(name, "snd_pcm_") || len(entries) != 2 {
			continue
		}
		var legacy, current *entry
		for i := range entries {
			// These indices belong to the fingerprinted library: hidden
			// ALSA_0.9 compatibility API versus default ALSA_0.9.0rc4 API.
			switch entries[i].version {
			case 0x8002:
				legacy = &entries[i]
			case 3:
				current = &entries[i]
			}
		}
		if legacy != nil && current != nil && data[legacy.offset+12]&15 == byte(elf.STT_FUNC) && data[current.offset+12]&15 == byte(elf.STT_FUNC) {
			// Only remove the obsolete alias from name lookup. Executable code,
			// shared-memory layout, and the original firmware file stay intact.
			binary.LittleEndian.PutUint32(patched[legacy.offset:], 0)
			changes++
		}
	}
	return patched, changes, nil
}
