package alsacompat

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func rateFixture(t *testing.T) (string, string, string, string, rateHashes) {
	t.Helper()
	root := t.TempDir()
	compat := filepath.Join(root, "compat-fixture")
	if err := os.Mkdir(compat, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(compat, "libasound.so.2"), filepath.Join(root, "full-samplerate"), filepath.Join(root, "rate-module")}
	data := [][]byte{[]byte("before\x00/lib/linkplay_a98/alsa-lib\x00after"), []byte("inert full library fixture"), []byte("inert module fixture")}
	for i, path := range paths {
		if err := os.WriteFile(path, data[i], 0600); err != nil {
			t.Fatal(err)
		}
	}
	hashes := rateHashes{fmt.Sprintf("%x", sha256.Sum256(data[0])), fmt.Sprintf("%x", sha256.Sum256(data[1])), fmt.Sprintf("%x", sha256.Sum256(data[2]))}
	return root, compat, paths[1], paths[2], hashes
}

func TestRatePreparationKeepsInputsPrivateAndReusable(t *testing.T) {
	root, compat, library, module, hashes := rateFixture(t)
	original, _ := os.ReadFile(filepath.Join(compat, "libasound.so.2"))
	dir, err := prepareRate(root, compat, library, module, hashes)
	if err != nil || dir == "" {
		t.Fatal(dir, err)
	}
	for _, name := range []string{"libasound.so.2", "libsamplerate.so.0"} {
		path := filepath.Join(dir, name)
		stat, err := os.Stat(path)
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatal("private output mode", stat, err)
		}
		if _, err := prepareRate(root, compat, library, module, hashes); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(path)
		if !stat.ModTime().Equal(after.ModTime()) {
			t.Fatal("rewrote an identical private file")
		}
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "libasound.so.2"))
	expected, _ := firmwareRatePluginPath(original)
	if !bytes.Equal(saved, expected) {
		t.Fatal("incorrect private ALSA copy")
	}
	unchanged, _ := os.ReadFile(filepath.Join(compat, "libasound.so.2"))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("modified the ABI compatibility input")
	}
	full, _ := os.ReadFile(library)
	copy, _ := os.ReadFile(filepath.Join(dir, "libsamplerate.so.0"))
	if !bytes.Equal(full, copy) {
		t.Fatal("changed the full converter library")
	}
}

func TestRatePreparationSkipsMissingOrUnverifiedInputs(t *testing.T) {
	for _, which := range []string{"no ABI repair", "ALSA", "library", "module", "missing library", "missing module"} {
		t.Run(which, func(t *testing.T) {
			root, compat, library, module, hashes := rateFixture(t)
			switch which {
			case "no ABI repair":
				compat = ""
			case "ALSA":
				hashes.alsa = "unknown"
			case "library":
				hashes.library = "unknown"
			case "module":
				hashes.module = "unknown"
			case "missing library":
				library += ".missing"
			case "missing module":
				module += ".missing"
			}
			if dir, err := prepareRate(root, compat, library, module, hashes); err != nil || dir != "" {
				t.Fatal("enabled sinc for an unverified profile", dir, err)
			}
			if _, err := os.Stat(filepath.Join(root, "alsa-rate")); !os.IsNotExist(err) {
				t.Fatal("wrote output for an unverified profile")
			}
		})
	}
}

func TestRatePathChangesOnlyTheBoundedDirectoryString(t *testing.T) {
	input := []byte("before\x00/lib/linkplay_a98/alsa-lib\x00after\x00/lib/linkplay_a98/alsa-lib/smixer\x00")
	before := append([]byte(nil), input...)
	output, err := firmwareRatePluginPath(input)
	expected := append([]byte("before\x00/system/workdir/lib"), make([]byte, 8)...)
	expected = append(expected, []byte("after\x00/lib/linkplay_a98/alsa-lib/smixer\x00")...)
	if err != nil || !bytes.Equal(output, expected) || !bytes.Equal(input, before) || len(output) != len(input) {
		t.Fatal("changed bytes outside the standalone directory string", err)
	}
	for _, invalid := range [][]byte{nil, []byte("/lib/linkplay_a98/alsa-lib/smixer\x00"), bytes.Repeat(input, 2)} {
		if _, err := firmwareRatePluginPath(invalid); err == nil {
			t.Fatal("accepted unexpected directory layout")
		}
	}
}

func TestRatePreparationReportsWriteFailure(t *testing.T) {
	root, compat, library, module, hashes := rateFixture(t)
	if err := os.WriteFile(filepath.Join(root, "alsa-rate"), []byte("blocked directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRate(root, compat, library, module, hashes); err == nil {
		t.Fatal("hid a preparation failure")
	}
}

func TestVerifiedRateFirmwareRegression(t *testing.T) {
	library, player := os.Getenv("AIRPLAY_TEST_ALSA"), os.Getenv("AIRPLAY_TEST_APLAY")
	rateLibrary, rateModule := os.Getenv("AIRPLAY_TEST_SAMPLERATE"), os.Getenv("AIRPLAY_TEST_RATE_MODULE")
	if library == "" || player == "" || rateLibrary == "" || rateModule == "" {
		t.Skip("private firmware fixtures not supplied")
	}
	root := t.TempDir()
	compat, err := Prepare(root, library, player)
	if err != nil || compat == "" {
		t.Fatal("missing verified ABI repair", compat, err)
	}
	dir, err := PrepareRate(root, compat, rateLibrary, rateModule)
	if err != nil || dir == "" {
		t.Fatal("verified sinc profile was not prepared", dir, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "libasound.so.2"))
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "7ffd9f3477457825191e0c074aec9fd02aec657e8f6ce3515bafeb1f4d4529a7" {
		t.Fatalf("differs from the ALSA copy tested on Mini: %s", got)
	}
}
