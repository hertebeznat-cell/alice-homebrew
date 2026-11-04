//go:build airplay

package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSharedRouteUnknownMini1FirmwareKeepsLinear(t *testing.T) {
	root := fakeFirmware(t, mini1Aplay, mini1ALSA, mini1MixerRoute, firmwareALSAConf)
	data := t.TempDir()
	route, err := findSharedRoute(root, data)
	if err != nil {
		t.Fatal(err)
	}
	if route.Device != "alice_shared" || !slices.Contains(route.Env, "LD_LIBRARY_PATH="+mini1Libraries) {
		t.Fatalf("unknown firmware changed its audio route: %+v", route)
	}
	config, err := os.ReadFile(filepath.Join(data, "shared-alsa.conf"))
	if err != nil || string(config) != fmt.Sprintf(sharedALSAConfig, "linear", "linear") {
		t.Fatalf("unknown firmware must retain linear conversion: %q, %v", config, err)
	}
	for _, dir := range []string{"alsa-compat", "alsa-rate"} {
		if _, err := os.Stat(filepath.Join(data, dir)); !os.IsNotExist(err) {
			t.Fatalf("created private library directory for unknown firmware: %s, %v", dir, err)
		}
	}
}

// Optional private fixtures exercise the complete beta.4 route selection,
// rather than just the library-patching helpers. No firmware is distributed.
func TestSharedRouteVerifiedMini1Firmware(t *testing.T) {
	rateLibrary := "/system/workdir/lib/libsamplerate.so.0.1.8"
	rateModule := "/system/workdir/lib/libasound_module_rate_samplerate_medium.so"
	fixtures := make(map[string][]byte)
	for path, variable := range map[string]string{
		mini1Aplay: "AIRPLAY_TEST_APLAY", mini1ALSA: "AIRPLAY_TEST_ALSA",
		rateLibrary: "AIRPLAY_TEST_SAMPLERATE", rateModule: "AIRPLAY_TEST_RATE_MODULE",
	} {
		source := os.Getenv(variable)
		if source == "" {
			t.Skip("optional private firmware fixtures are not configured")
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		fixtures[path] = data
	}
	for _, test := range []struct {
		name         string
		mixer, rate  bool
		device, conv string
	}{
		{"sinc", true, true, "alice_shared", "samplerate_medium"},
		{"missing-rate-falls-back", true, false, "alice_shared", "linear"},
		{"firmware-default-without-mixer", false, true, "default", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fakeFirmware(t)
			if test.mixer {
				root = fakeFirmware(t, mini1MixerRoute, firmwareALSAConf)
			}
			for path, data := range fixtures {
				if !test.rate && (path == rateLibrary || path == rateModule) {
					continue
				}
				target := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, data, 0755); err != nil {
					t.Fatal(err)
				}
			}
			data := t.TempDir()
			route, err := findSharedRoute(root, data)
			if err != nil {
				t.Fatal(err)
			}
			libraries := filepath.Join(data, "alsa-compat") + ":" + mini1Libraries
			if test.conv == "samplerate_medium" {
				libraries = filepath.Join(data, "alsa-rate") + ":" + libraries
				patched, err := os.ReadFile(filepath.Join(data, "alsa-rate/libasound.so.2"))
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(patched)) != "7ffd9f3477457825191e0c074aec9fd02aec657e8f6ce3515bafeb1f4d4529a7" {
					t.Fatalf("route did not prepare the listening-tested library: %v", err)
				}
			} else if _, err := os.Stat(filepath.Join(data, "alsa-rate")); !os.IsNotExist(err) {
				t.Fatalf("prepared sinc for a route that cannot select it: %v", err)
			}
			if route.Device != test.device || !slices.Contains(route.Env, "LD_LIBRARY_PATH="+libraries) {
				t.Fatalf("incorrect private library search path: %+v", route)
			}
			configPath := filepath.Join(data, "shared-alsa.conf")
			config, err := os.ReadFile(configPath)
			if test.mixer {
				if err != nil || string(config) != fmt.Sprintf(sharedALSAConfig, test.conv, test.conv) || !slices.Contains(route.Env, "ALSA_CONFIG_PATH="+configPath) {
					t.Fatalf("incorrect converter configuration: %q, %v, %+v", config, err, route)
				}
			} else if !os.IsNotExist(err) || len(route.Env) != 1 {
				t.Fatalf("overrode the firmware's default ALSA configuration: %+v, %v", route, err)
			}
			for path, want := range fixtures {
				if !test.rate && (path == rateLibrary || path == rateModule) {
					continue
				}
				got, err := os.ReadFile(filepath.Join(root, path))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("modified firmware file %s: %v", path, err)
				}
			}
		})
	}
}
