package homebrew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DeviceProfile names the station Homebrew runs on. IDs match the yam
// platform profiles so the same identifier selects Homebrew plugins and
// yam packages.
type DeviceProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Model    string `json:"model,omitempty"`
	Arch     string `json:"arch"`
	LEDRing  bool   `json:"led_ring"`
	Known    bool   `json:"known"`
	Detected string `json:"detected"`
	Identity string `json:"identity,omitempty"`
}

type platformInfo struct {
	ID, RU, EN, Arch string
	// LEDRing marks models with a known ring driver (Mini 1 I2C, or the
	// sysfs layouts yamd drove). The station confirms it at startup.
	LEDRing  bool
	Patterns []string
}

// Order matters: the most specific patterns come first, as in yam install.sh.
var platforms = []platformInfo{
	{"yandexmini_2", "Станция Мини 2", "Station Mini 2", "aarch64", true, []string{"yandex,station-mini2", "station-mini2"}},
	{"cucumber", "Станция Миди", "Station Midi", "aarch64", true, []string{"yandex,cucumber-dvt2", "cucumber"}},
	{"yandexmidi", "Станция 2", "Station 2", "aarch64", false, []string{"yandex,station-midi"}},
	{"orion", "Станция 3", "Station 3", "aarch64", false, []string{"yandex,orion-pvt", "orion"}},
	{"pickle", "Станция Мини 3 Про", "Station Mini 3 Pro", "aarch64", false, []string{"yandex,pickle-evt", "pickle"}},
	{"bergamot", "Станция Мини 3", "Station Mini 3", "aarch64", false, []string{"yandex,station-mini3", "bergamot"}},
	{"saturn", "Умный хаб", "Smart Hub", "aarch64", false, []string{"yandex,saturn", "saturn"}},
	{"yandexstation", "Станция (1-е поколение)", "Station (1st gen)", "aarch64", false, []string{"tulip-d1", "tulip"}},
	{"yandexstation_2", "Станция Макс", "Station Max", "aarch64", false, []string{"yandexstation_2", "station-max"}},
	{"jbl_link_music", "JBL Link Music", "JBL Link Music", "armv7", false, []string{"mediatek,mt8167", "jbl"}},
	{"mango", "Станция Стрит", "Station Street", "armv7", false, []string{"mango"}},
	{"plum", "Станция Лайт 2", "Station Lite 2", "aarch64", true, []string{"plum"}},
	{"yandexmicro", "Станция Лайт", "Station Lite", "aarch64", true, []string{"yandex,station-micro", "station-micro", "yandexmicro"}},
	{"yandexmini", "Станция Мини (1-е поколение)", "Station Mini (1st gen)", "aarch64", true, []string{"amlogic,axg", "yndx-0004"}},
}

// PlatformIDs lists every supported station ID for --platform.
func PlatformIDs() []string {
	ids := make([]string, len(platforms))
	for i, p := range platforms {
		ids[i] = p.ID
	}
	return ids
}

func platformByID(id string) (platformInfo, bool) {
	if id == "yandexplum" {
		id = "plum"
	}
	for _, p := range platforms {
		if p.ID == id {
			return p, true
		}
	}
	return platformInfo{}, false
}

func hostArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7"
	case "amd64":
		return "x86_64"
	}
	return runtime.GOARCH
}

func profileFor(p platformInfo, detected, identity string) DeviceProfile {
	return DeviceProfile{ID: p.ID, Name: p.RU + "|" + p.EN, Arch: p.Arch, LEDRing: p.LEDRing, Known: true, Detected: detected, Identity: identity}
}

// MatchPlatform maps getprop/device-tree text to a station profile.
func MatchPlatform(identity string) (platformInfo, bool) {
	// Device trees differ in spacing: Mini 1 reports "amlogic, axg".
	identity = strings.ReplaceAll(strings.ToLower(identity), " ", "")
	for _, p := range platforms {
		for _, pattern := range p.Patterns {
			if strings.Contains(identity, pattern) {
				return p, true
			}
		}
	}
	return platformInfo{}, false
}

// DetectDevice reads the station identity. An explicit override wins; on a
// computer without one the manager reports a preview device.
func DetectDevice(override string) (DeviceProfile, error) {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "arm") && IsAndroid("/") {
		return DeviceProfile{}, errors.New("Android-based stations are not supported by Alice Homebrew")
	}
	if override != "" {
		p, ok := platformByID(override)
		if !ok {
			return DeviceProfile{}, fmt.Errorf("unknown platform %q; use one of: %s", override, strings.Join(PlatformIDs(), ", "))
		}
		d := profileFor(p, "flag", "")
		d.Model = readModel()
		return d, nil
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "arm") {
		return DeviceProfile{ID: "preview", Name: "Предпросмотр на компьютере|Computer preview", Arch: hostArch(), Detected: "preview"}, nil
	}
	identity := readIdentity()
	model := readModel()
	if p, ok := MatchPlatform(identity + "\n" + model); ok {
		d := profileFor(p, "device-tree", firstLine(identity))
		d.Model = model
		return d, nil
	}
	name := "Неизвестная станция|Unknown station"
	if model != "" {
		name = model + "|" + model
	}
	return DeviceProfile{ID: "unknown", Name: name, Model: model, Arch: hostArch(), Detected: "device-tree", Identity: firstLine(identity)}, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func readIdentity() string {
	b, _ := os.ReadFile("/proc/device-tree/compatible")
	parts := []string{strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", "\n")}
	if v := getprop("ro.product.device"); v != "" {
		parts = append(parts, v)
	}
	return strings.ToLower(strings.TrimSpace(strings.Join(parts, "\n")))
}

func readModel() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return getprop("ro.product.model")
}

func getprop(name string) string {
	path, e := exec.LookPath("getprop")
	if e != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, path, name).Output()
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Compatibility explains whether a Homebrew plugin fits this station.
// Blocking reasons refuse installation; notes are shown but do not block.
func (d DeviceProfile) Compatibility(m Manifest) (blocking, note string) {
	if len(m.Devices) > 0 && d.Known && !contains(m.Devices, d.ID) {
		return "not built for this station", ""
	}
	if m.Engine == "native" && d.Known && d.Arch != "aarch64" {
		return "native plugins require an ARM64 station", ""
	}
	if Has(m, "led.ring") && d.Known && !d.LEDRing {
		note = "this station has no supported LED ring"
	}
	return "", note
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// PreviewRingLayout is the LED arrangement a computer preview draws.
func PreviewRingLayout(id string) RingLayout {
	switch id {
	case "yandexmicro":
		return RingLayout{ID: "micro", Count: 3}
	case "plum", "yandexplum":
		return RingLayout{ID: "plum", Count: 3}
	case "yandexmini_2":
		return RingLayout{ID: "mini2", Count: 6}
	case "cucumber":
		return RingLayout{ID: "midi", Count: 24}
	}
	return mini1Layout
}

// IsAndroid reports an Android system: Homebrew targets the Linux
// (buildroot) stations and refuses Android-based ones.
func IsAndroid(root string) bool {
	for _, marker := range []string{"system/framework/framework.jar", "system/bin/app_process"} {
		if _, e := os.Stat(filepath.Join(root, marker)); e == nil {
			return true
		}
	}
	return false
}
