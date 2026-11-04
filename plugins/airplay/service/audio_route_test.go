//go:build airplay

package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeFirmware(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSharedRouteMini1UsesAliceMixer(t *testing.T) {
	root := fakeFirmware(t, mini1Aplay, mini1MixerRoute, firmwareALSAConf)
	route, err := findSharedRoute(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if route.Device != "alice_shared" || !strings.HasSuffix(route.Player, filepath.FromSlash(mini1Aplay)) {
		t.Fatalf("%+v", route)
	}
	if !slices.Contains(route.Env, "LD_LIBRARY_PATH="+mini1Libraries) {
		t.Fatalf("Mini 1 player needs its firmware libraries: %v", route.Env)
	}
}

func TestSharedRouteOtherStationUsesFirmwareDefault(t *testing.T) {
	root := fakeFirmware(t, "/usr/bin/aplay")
	route, err := findSharedRoute(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if route.Device != "default" || len(route.Env) != 0 {
		t.Fatalf("must keep the firmware ALSA setup untouched: %+v", route)
	}
}

func TestSharedRouteWithoutAplayExplainsDirectMode(t *testing.T) {
	_, err := findSharedRoute(fakeFirmware(t), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Direct output") {
		t.Fatalf("expected a hint about direct mode, got %v", err)
	}
}

func TestDirectDevice(t *testing.T) {
	pcm := "00-00: TDM-B-dummy-alsaPORT-pcm dummy-0 :  : playback 1 : capture 1\n" +
		"00-02: PDM-dummy : : capture 1\n" +
		"01-03: HDMI : : playback 1\n"
	if got := playbackDevices(pcm); !slices.Equal(got, []string{"0,0", "1,3"}) {
		t.Fatal(got)
	}
	for setting, want := range map[string]string{"auto": "0,0", "": "0,0", "plughw:1,3": "1,3", "plughw:0,1": "0,1"} {
		if got, err := directDevice(setting, pcm); err != nil || got != want {
			t.Fatalf("%q: %q %v", setting, got, err)
		}
	}
	for _, bad := range []string{"hw:0,0", "plughw:0", "plughw:x,1", "plughw:0,1;rm", "plughw:999,0"} {
		if validOutput(bad) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := directDevice("auto", "00-02: PDM : : capture 1\n"); err == nil {
		t.Fatal("capture-only card accepted")
	}
}

func TestLANInterfacePrefersWiFiAndNeverUSB(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ip := func(s string) []net.IP { return []net.IP{net.ParseIP(s).To4()} }
	usb := candidate{iface: net.Interface{Name: "usb0", Flags: up}, ips: ip("192.168.7.1")}
	wifi := candidate{iface: net.Interface{Name: "wlan0", Flags: up}, ips: ip("192.168.31.175")}
	wired := candidate{iface: net.Interface{Name: "eth0", Flags: up}, ips: ip("10.0.0.5")}
	loop := candidate{iface: net.Interface{Name: "lo", Flags: up | net.FlagLoopback}, ips: ip("127.0.0.1")}
	if got := lanInterface([]candidate{usb, loop, wired, wifi}); got == nil || got.iface.Name != "wlan0" {
		t.Fatalf("want wlan0, got %+v", got)
	}
	if got := lanInterface([]candidate{usb, wired}); got == nil || got.iface.Name != "eth0" {
		t.Fatalf("want eth0, got %+v", got)
	}
	// Wi-Fi without an address yet (boot): wait instead of advertising USB.
	noAddr := candidate{iface: net.Interface{Name: "wlan0", Flags: up}}
	if got := lanInterface([]candidate{usb, noAddr, loop}); got != nil {
		t.Fatalf("advertised an unreachable interface: %+v", got)
	}
}
