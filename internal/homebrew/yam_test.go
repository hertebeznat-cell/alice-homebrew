package homebrew

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatchPlatform(t *testing.T) {
	for identity, want := range map[string]string{
		"amlogic, axg":                      "yandexmini",
		"yandex, station-mini2":             "yandexmini_2",
		"yandex,station-mini2\namlogic,axg": "yandexmini_2",
		"yandex,cucumber-dvt2":              "cucumber",
		"mediatek,mt8167":                   "jbl_link_music",
		"mango":                             "mango",
		"YNDX-0004":                         "yandexmini",
	} {
		p, ok := MatchPlatform(identity)
		if !ok || p.ID != want {
			t.Fatalf("%q: got %q, want %q", identity, p.ID, want)
		}
	}
	if _, ok := MatchPlatform("generic,board"); ok {
		t.Fatal("unknown board matched")
	}
	d, e := DetectDevice("yandexplum")
	if e != nil || d.ID != "plum" || !d.Known {
		t.Fatalf("legacy plum alias: %+v %v", d, e)
	}
	if _, e = DetectDevice("toaster"); e == nil {
		t.Fatal("unknown override accepted")
	}
}

func TestDeviceCompatibility(t *testing.T) {
	mini, _ := DetectDevice("yandexmini")
	street, _ := DetectDevice("mango")
	midi, _ := DetectDevice("cucumber")
	station2, _ := DetectDevice("yandexmidi")
	ring := Manifest{Permissions: []string{"led.ring"}}
	if b, n := mini.Compatibility(ring); b != "" || n != "" {
		t.Fatalf("mini ring: %q %q", b, n)
	}
	if b, n := station2.Compatibility(ring); b != "" || n == "" {
		t.Fatalf("station without a ring should install with a note: %q %q", b, n)
	}
	if _, n := midi.Compatibility(ring); n != "" {
		t.Fatalf("midi has a ring: %q", n)
	}
	if b, _ := street.Compatibility(Manifest{Engine: "native"}); b == "" {
		t.Fatal("native plugin accepted on armv7")
	}
	if b, _ := midi.Compatibility(Manifest{Devices: []string{"yandexmini"}}); b == "" {
		t.Fatal("device list ignored")
	}
	preview := DeviceProfile{ID: "preview"}
	if b, _ := preview.Compatibility(Manifest{Devices: []string{"yandexmini"}, Engine: "native"}); b != "" {
		t.Fatal("preview must not block")
	}
	m := manifest("1.0.0")
	m.Devices = []string{"yandexmini", "nope"}
	if ValidateManifest(m) == nil {
		t.Fatal("unknown device accepted in manifest")
	}
}

func TestYamV2IndexSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte(`{"format":2,"packages":[{"name":"vegych/alice-pro-led","latest":"1.2.1","summary":"LED","homepage":null,"targets":["yandexmini","cucumber"],"stars":2},{"name":"yam/luna","latest":"0.2.2","summary":"Lua","targets":["cucumber"]},{"name":"x/any","latest":"1","summary":"","targets":["any"]},{"name":"../bad","latest":"1","summary":"","targets":["any"]}]}`)
	key := "ed25519:" + base64.StdEncoding.EncodeToString(pub)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))
	if e := VerifyYamIndex(body, sig, key); e != nil {
		t.Fatal(e)
	}
	tampered := append([]byte{}, body...)
	tampered[10] ^= 1
	if VerifyYamIndex(tampered, sig, key) == nil {
		t.Fatal("tampered index verified")
	}
	if VerifyYamIndex(body, sig, YamV2Key) == nil {
		t.Fatal("foreign key verified")
	}
	mini, _ := DetectDevice("yandexmini")
	packages, e := ParseYamV2Index(body, mini)
	if e != nil || len(packages) != 3 {
		t.Fatalf("packages: %+v %v", packages, e)
	}
	byName := map[string]YamPackage{}
	for _, p := range packages {
		byName[p.Name] = p
	}
	if !byName["vegych/alice-pro-led"].Compatible || byName["yam/luna"].Compatible || !byName["x/any"].Compatible {
		t.Fatalf("compatibility: %+v", byName)
	}
}

func TestYamV1Index(t *testing.T) {
	body := []byte(`{"format":1,"packages":[
	 {"id":"nano","version":"7.2","name":"nano","description":"old","models":[],"features":["arch.aarch64"],"url":"x","sha256":"x"},
	 {"id":"nano","version":"8.0","name":"nano","description":"Editor","models":[],"features":["arch.aarch64"],"url":"x","sha256":"x"},
	 {"id":"air","version":"1.4.0","models":["plum","yandexmidi"],"features":[]},
	 {"id":"air","version":"1.1.5","models":["yandexmini"],"features":["led.ring.rgb"]},
	 {"id":"bad id","version":"1"}],"latest":{}}`)
	mini, _ := DetectDevice("yandexmini")
	packages, e := ParseYamV1Index(body, mini)
	if e != nil || len(packages) != 2 {
		t.Fatalf("%+v %v", packages, e)
	}
	if packages[0].Name != "air" || packages[0].Version != "1.1.5" || !packages[0].Compatible || len(packages[0].Features) != 1 {
		t.Fatalf("air: %+v", packages[0])
	}
	if packages[1].Version != "8.0" || !packages[1].Compatible {
		t.Fatalf("nano: %+v", packages[1])
	}
	street, _ := DetectDevice("mango")
	packages, _ = ParseYamV1Index(body, street)
	if packages[1].Compatible || packages[1].Reason != "arch" {
		t.Fatalf("armv7 nano: %+v", packages[1])
	}
}

func TestYamJobs(t *testing.T) {
	device, _ := DetectDevice("yandexmini")
	y := NewYam(device)
	y.Root = t.TempDir()
	var calls []string
	y.run = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "list" {
			return "vegych/alice-pro-led\t1.2.1\tyandexmini\n", nil
		}
		return "ok\n", nil
	}
	y.Daemon = "http://127.0.0.1:1"
	y.registries["v2"].Packages = []YamPackage{{Registry: "v2", Name: "vegych/alice-pro-led", Compatible: true}, {Registry: "v2", Name: "yam/luna"}}
	if _, e := y.Start("install", "v2", "vegych/alice-pro-led"); e == nil {
		t.Fatal("install without yam runtime accepted")
	}
	if e := writeAtomic(y.wrapper()+"", []byte("#!/bin/sh\n"), 0755); e == nil {
		t.Fatal("wrapper directory should not exist yet")
	}
	installFakeWrapper(t, y)
	if _, e := y.Start("install", "v2", "yam/luna"); e == nil {
		t.Fatal("incompatible package accepted")
	}
	if _, e := y.Start("install", "v2", "evil; rm -rf /"); e == nil {
		t.Fatal("invalid name accepted")
	}
	job, e := y.Start("install", "v2", "vegych/alice-pro-led")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = y.Start("install", "v2", "vegych/alice-pro-led"); e == nil {
		t.Fatal("parallel job accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for y.Jobs()["busy"].(bool) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	jobs := y.Jobs()["jobs"].([]YamJob)
	if jobs[0].ID != job.ID || jobs[0].State != "done" {
		t.Fatalf("job: %+v", jobs[0])
	}
	if strings.Join(calls, "|") != "registry add official "+YamV2Registry+" --key "+YamV2Key+"|install --yes vegych/alice-pro-led|list" {
		t.Fatalf("calls: %q", calls)
	}
	snap := y.Snapshot()["registries"].([]YamRegistry)
	if snap[0].Packages[0].Installed != "1.2.1" {
		t.Fatalf("installed version missing: %+v", snap[0].Packages[0])
	}
	if _, e = y.Start("remove", "v2", "yam/luna"); e == nil {
		t.Fatal("removing a package that is not installed accepted")
	}
}

func installFakeWrapper(t *testing.T, y *Yam) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(y.wrapper()), 0755); e != nil {
		t.Fatal(e)
	}
	if e := writeAtomic(y.wrapper(), []byte("#!/bin/sh\n"), 0755); e != nil {
		t.Fatal(e)
	}
}

func TestYamLocalPackage(t *testing.T) {
	device, _ := DetectDevice("yandexmini")
	y := NewYam(device)
	y.Root = t.TempDir()
	y.Daemon = "http://127.0.0.1:1"
	var args []string
	y.run = func(ctx context.Context, name string, a ...string) (string, error) {
		if a[0] == "install" {
			args = a
			if _, e := os.Stat(a[2]); e != nil {
				t.Errorf("package file missing during install: %v", e)
			}
		}
		return "Installed x/y 1.0.0 for yandexmini.\n", nil
	}
	zstd := append([]byte{0x28, 0xb5, 0x2f, 0xfd}, make([]byte, 32)...)
	if _, e := y.StartLocal("p.yampkg", zstd); e == nil {
		t.Fatal("install without yam v2 accepted")
	}
	installFakeWrapper(t, y)
	if _, e := y.StartLocal("p.yampkg", []byte("PK not zstd")); e == nil {
		t.Fatal("wrong archive accepted as .yampkg")
	}
	if _, e := y.StartLocal("p.exe", zstd); e == nil {
		t.Fatal("unknown extension accepted")
	}
	if _, e := y.StartLocal("p.yap", []byte("PK..")); e == nil {
		t.Fatal(".yap accepted without yamd")
	}
	job, e := y.StartLocal("../../p.yampkg", zstd)
	if e != nil || job.Name != "p.yampkg" {
		t.Fatalf("%+v %v", job, e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for y.Jobs()["busy"].(bool) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(args) != 3 || args[1] != "--yes" || !strings.HasSuffix(args[2], ".yampkg") {
		t.Fatalf("args: %q", args)
	}
	if _, e := os.Stat(args[2]); !os.IsNotExist(e) {
		t.Fatal("uploaded package left on disk")
	}
	if y.Jobs()["jobs"].([]YamJob)[0].State != "done" {
		t.Fatal("job not done")
	}
}

func TestYamAutoSetupOnce(t *testing.T) {
	device, _ := DetectDevice("yandexmini")
	y := NewYam(device)
	y.station, y.Root, y.Daemon = true, t.TempDir(), "http://127.0.0.1:1"
	y.run = func(ctx context.Context, name string, a ...string) (string, error) { return "", nil }
	var order []string
	y.setupHook = func(ctx context.Context, reg string) (string, error) {
		order = append(order, reg)
		if reg == "v2" {
			installFakeWrapper(t, y)
			return "ok", nil
		}
		return "", errors.New("yamd did not start")
	}
	record := filepath.Join(t.TempDir(), "yam-autosetup.json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	y.AutoSetup(ctx, record)
	if strings.Join(order, ",") != "v2,v1" {
		t.Fatalf("order %v", order)
	}
	b, _ := os.ReadFile(record)
	if !strings.Contains(string(b), `"v2": "done"`) || !strings.Contains(string(b), `"v1": "error"`) {
		t.Fatalf("record %s", b)
	}
	y.AutoSetup(ctx, record)
	if len(order) != 2 {
		t.Fatalf("auto setup repeated: %v", order)
	}
	street, _ := DetectDevice("mango")
	z := NewYam(street)
	z.station = true
	if z.runtimeLocked("v1").CanSetup {
		t.Fatal("yamd offered on an armv7 station")
	}
	pc := NewYam(device)
	pc.station = false
	pc.AutoSetup(ctx, filepath.Join(t.TempDir(), "r.json"))
}

func TestIsAndroid(t *testing.T) {
	root := t.TempDir()
	if IsAndroid(root) {
		t.Fatal("empty root reported as Android")
	}
	os.MkdirAll(filepath.Join(root, "system", "bin"), 0755)
	os.WriteFile(filepath.Join(root, "system", "bin", "app_process"), nil, 0755)
	if !IsAndroid(root) {
		t.Fatal("Android marker missed")
	}
}
