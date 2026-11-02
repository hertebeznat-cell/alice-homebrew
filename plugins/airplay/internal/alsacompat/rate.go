package alsacompat

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

type rateHashes struct {
	alsa, library, module string
}

// PrepareRate enables sinc conversion only for the ABI-repaired Mini 1 library
// and the full firmware libsamplerate/module pair verified with it. An empty
// result leaves the existing linear converter in use on other firmware.
func PrepareRate(root, compat, library, module string) (string, error) {
	return prepareRate(root, compat, library, module, rateHashes{
		alsa:    "fa6d683ab80a5dcc6edf240aa4148495d074b847f79e731a5c1599cd8ee84d44",
		library: "015cb1d352cd4423d902a54fc3a875ab1910f7c1e02a3740d49a7c5c612ada8b",
		module:  "6c33f7d5a411ba29010ebc70fb2852cb70dcd5280db451cf622102552e145002",
	})
}

func prepareRate(root, compat, library, module string, hashes rateHashes) (string, error) {
	if compat == "" {
		return "", nil
	}
	paths := []string{filepath.Join(compat, "libasound.so.2"), library, module}
	expected := []string{hashes.alsa, hashes.library, hashes.module}
	files := make([][]byte, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != expected[i] {
			return "", nil
		}
		files[i] = data
	}
	patched, err := firmwareRatePluginPath(files[0])
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "alsa-rate")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// The vendor library search path contains a reduced libsamplerate build.
	// Prepend a private full copy for this aplay child only, leaving Alice alone.
	for name, data := range map[string][]byte{"libasound.so.2": patched, "libsamplerate.so.0": files[1]} {
		if err := writePrivateRateFile(filepath.Join(dir, name), data); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func firmwareRatePluginPath(data []byte) ([]byte, error) {
	old := []byte("/lib/linkplay_a98/alsa-lib\x00")
	if bytes.Count(data, old) != 1 {
		return nil, fmt.Errorf("unexpected verified ALSA plugin directory")
	}
	// This old ALSA build ignores ALSA_PLUGIN_DIR. Change only the standalone
	// directory string in our private copy; retain offsets, code and dmix ABI.
	path := make([]byte, len(old))
	copy(path, "/system/workdir/lib")
	return bytes.Replace(data, old, path, 1), nil
}

func writePrivateRateFile(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		if stat, err := os.Stat(path); err == nil && stat.Mode().Perm() == 0600 {
			return nil
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "rate-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
