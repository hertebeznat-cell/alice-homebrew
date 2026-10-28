package homebrew

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// yam (github.com/nyakokitsu/yam) is a separate package manager for Yandex
// stations. Homebrew shows its registries next to Homebrew plugins and drives
// the yam runtimes already present on the station; it never unpacks .yampkg
// or .yap archives itself.
const (
	YamV2Registry = "https://repo.yse.wtf/v2"
	YamV2Key      = "ed25519:5Rz8F47uftWVU5kLgWYhprkEZ4rpP2kecUge/a7KQas="
	YamV1Index    = "https://raw.githubusercontent.com/nyakokitsu/yapkg/registry/index.json"
	YamV1Daemon   = "http://127.0.0.1:8099"
	yamReleases   = "https://api.github.com/repos/nyakokitsu/yam/releases?per_page=30"
	yamRawBase    = "https://raw.githubusercontent.com/nyakokitsu/yam/"
)

var (
	yamV2NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}/[a-z0-9][a-z0-9._-]{0,63}$`)
	yamV1NameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	yamTagRE    = regexp.MustCompile(`^yam-v[0-9]+\.[0-9]+\.[0-9]+$`)
)

type YamPackage struct {
	Registry   string   `json:"registry"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Summary    string   `json:"summary"`
	Homepage   string   `json:"homepage,omitempty"`
	Targets    []string `json:"targets,omitempty"`
	Features   []string `json:"features,omitempty"`
	Stars      int      `json:"stars,omitempty"`
	Compatible bool     `json:"compatible"`
	Reason     string   `json:"reason,omitempty"`
	Installed  string   `json:"installed,omitempty"`
}
type YamRuntime struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CanSetup  bool   `json:"can_setup"`
}
type YamRegistry struct {
	ID       string       `json:"id"`
	URL      string       `json:"url"`
	Signed   bool         `json:"signed"`
	Verified bool         `json:"verified"`
	Error    string       `json:"error,omitempty"`
	Updated  time.Time    `json:"updated_at"`
	Runtime  YamRuntime   `json:"runtime"`
	Packages []YamPackage `json:"packages"`
}
type YamJob struct {
	ID       string    `json:"id"`
	Action   string    `json:"action"`
	Registry string    `json:"registry"`
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Output   string    `json:"output,omitempty"`
	Started  time.Time `json:"started_at"`
	Finished time.Time `json:"finished_at"`
}

type Yam struct {
	mu         sync.Mutex
	Device     DeviceProfile
	Root       string
	LegacyRoot string
	Daemon     string
	client     *http.Client
	run        func(ctx context.Context, name string, args ...string) (string, error)
	registries map[string]*YamRegistry
	installed  map[string]map[string]string
	jobs       []*YamJob
	busy       bool
	refreshing bool
	// station is true on a Linux ARM station; tests and setupHook stand in
	// for the network installers.
	station   bool
	setupHook func(ctx context.Context, registry string) (string, error)
}

func NewYam(device DeviceProfile) *Yam {
	y := &Yam{Device: device, station: runtime.GOOS == "linux" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "arm"), Root: "/data/yam", LegacyRoot: "/data/quazar/yam", Daemon: YamV1Daemon, run: runCommand,
		client:     &http.Client{Transport: trustedHTTPTransport(), Timeout: 60 * time.Second},
		registries: map[string]*YamRegistry{"v2": {ID: "v2", URL: YamV2Registry, Signed: true, Packages: []YamPackage{}}, "v1": {ID: "v1", URL: YamV1Index, Packages: []YamPackage{}}},
		installed:  map[string]map[string]string{"v2": {}, "v1": {}}}
	return y
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
	s := tailText(strings.ToValidUTF8(string(out), "?"), 8192)
	if e != nil {
		if strings.TrimSpace(s) == "" {
			s = e.Error()
		}
		return s, errors.New(lastLine(s))
	}
	return s, nil
}
func tailText(s string, n int) string {
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (y *Yam) wrapper() string { return filepath.Join(y.Root, "bin", "yam-run") }

func (y *Yam) Snapshot() map[string]any {
	y.mu.Lock()
	defer y.mu.Unlock()
	registries := []YamRegistry{}
	for _, id := range []string{"v2", "v1"} {
		r := clone(*y.registries[id])
		r.Runtime = y.runtimeLocked(id)
		for i := range r.Packages {
			r.Packages[i].Installed = y.installed[id][r.Packages[i].Name]
		}
		registries = append(registries, r)
	}
	jobs := []YamJob{}
	for i := len(y.jobs) - 1; i >= 0; i-- {
		jobs = append(jobs, *y.jobs[i])
	}
	installed := map[string]map[string]string{}
	for id, list := range y.installed {
		installed[id] = map[string]string{}
		for name, version := range list {
			installed[id][name] = version
		}
	}
	return map[string]any{"registries": registries, "installed": installed, "jobs": jobs, "busy": y.busy, "refreshing": y.refreshing}
}

func (y *Yam) runtimeLocked(id string) YamRuntime {
	stationOS := y.station
	if id == "v2" {
		if s, e := os.Stat(y.wrapper()); e == nil && s.Mode().IsRegular() && (runtime.GOOS == "windows" || s.Mode()&0111 != 0) {
			return YamRuntime{Available: true, Path: y.wrapper()}
		}
		reason := "not-installed"
		if !stationOS {
			reason = "preview"
		} else if !y.Device.Known {
			reason = "unknown-device"
		}
		return YamRuntime{Reason: reason, CanSetup: stationOS && y.Device.Known}
	}
	if y.registries["v1"].Runtime.Available {
		return y.registries["v1"].Runtime
	}
	if !stationOS {
		return YamRuntime{Reason: "preview"}
	}
	// yamd is published for ARM64 only.
	return YamRuntime{Reason: "yamd-not-running", CanSetup: y.Device.Known && y.Device.Arch == "aarch64"}
}

// Jobs is the light status used by the UI's periodic poll.
func (y *Yam) Jobs() map[string]any {
	y.mu.Lock()
	defer y.mu.Unlock()
	jobs := []YamJob{}
	for i := len(y.jobs) - 1; i >= 0; i-- {
		jobs = append(jobs, *y.jobs[i])
	}
	return map[string]any{"jobs": jobs, "busy": y.busy}
}

// Refresh downloads both registries and reads what the station has installed.
func (y *Yam) Refresh(ctx context.Context) {
	y.mu.Lock()
	if y.refreshing {
		y.mu.Unlock()
		return
	}
	y.refreshing = true
	y.mu.Unlock()
	defer func() { y.mu.Lock(); y.refreshing = false; y.mu.Unlock() }()
	var wg sync.WaitGroup
	results := map[string]struct {
		packages []YamPackage
		err      error
	}{}
	var rmu sync.Mutex
	for id, fetch := range map[string]func(context.Context) ([]YamPackage, error){"v2": y.fetchV2, "v1": y.fetchV1} {
		wg.Add(1)
		go func(id string, fetch func(context.Context) ([]YamPackage, error)) {
			defer wg.Done()
			p, e := fetch(ctx)
			rmu.Lock()
			results[id] = struct {
				packages []YamPackage
				err      error
			}{p, e}
			rmu.Unlock()
		}(id, fetch)
	}
	wg.Wait()
	y.mu.Lock()
	for id, r := range results {
		reg := y.registries[id]
		if r.err != nil {
			reg.Error = r.err.Error()
			continue
		}
		reg.Error, reg.Packages, reg.Updated, reg.Verified = "", r.packages, time.Now().UTC(), reg.Signed
	}
	y.mu.Unlock()
	y.refreshInstalled(ctx)
}

func (y *Yam) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "alice-homebrew/"+Version)
	res, e := y.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, url)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if e == nil && int64(len(b)) > limit {
		e = errors.New("response is too large: " + url)
	}
	return b, e
}

// VerifyYamIndex checks a detached Base64 Ed25519 signature over exact bytes.
func VerifyYamIndex(body []byte, signature, key string) error {
	raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(key), "ed25519:"))
	if e != nil || len(raw) != ed25519.PublicKeySize {
		return errors.New("invalid yam registry key")
	}
	sig, e := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if e != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(raw), body, sig) {
		return errors.New("yam registry signature does not match the pinned key")
	}
	return nil
}

func (y *Yam) fetchV2(ctx context.Context) ([]YamPackage, error) {
	body, e := y.get(ctx, YamV2Registry+"/index.json", 8<<20)
	if e != nil {
		return nil, e
	}
	sig, e := y.get(ctx, YamV2Registry+"/index.json.sig", 1024)
	if e != nil {
		return nil, e
	}
	if e = VerifyYamIndex(body, string(sig), YamV2Key); e != nil {
		return nil, e
	}
	return ParseYamV2Index(body, y.Device)
}

func ParseYamV2Index(body []byte, d DeviceProfile) ([]YamPackage, error) {
	var index struct {
		Packages []struct {
			Name     string   `json:"name"`
			Latest   string   `json:"latest"`
			Summary  string   `json:"summary"`
			Homepage *string  `json:"homepage"`
			Targets  []string `json:"targets"`
			Stars    int      `json:"stars"`
		} `json:"packages"`
	}
	if e := json.Unmarshal(body, &index); e != nil {
		return nil, fmt.Errorf("invalid yam v2 index: %w", e)
	}
	out := []YamPackage{}
	for _, p := range index.Packages {
		if !yamV2NameRE.MatchString(p.Name) || len(out) >= 2000 {
			continue
		}
		v := YamPackage{Registry: "v2", Name: p.Name, Version: limitText(p.Latest, 40), Summary: limitText(p.Summary, 400), Targets: p.Targets, Stars: p.Stars, Compatible: true}
		if p.Homepage != nil && strings.HasPrefix(*p.Homepage, "https://") {
			v.Homepage = limitText(*p.Homepage, 300)
		}
		if d.Known && !contains(p.Targets, "any") && !contains(p.Targets, d.ID) && !(d.ID == "plum" && contains(p.Targets, "yandexplum")) {
			v.Compatible, v.Reason = false, "target"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (y *Yam) fetchV1(ctx context.Context) ([]YamPackage, error) {
	body, e := y.get(ctx, YamV1Index, 8<<20)
	if e != nil {
		return nil, e
	}
	return ParseYamV1Index(body, y.Device)
}

// ParseYamV1Index keeps the newest build per package, preferring builds that
// fit this station, as the legacy yamd resolver does.
func ParseYamV1Index(body []byte, d DeviceProfile) ([]YamPackage, error) {
	var index struct {
		Packages []struct {
			ID          string   `json:"id"`
			Version     string   `json:"version"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Models      []string `json:"models"`
			Features    []string `json:"features"`
		} `json:"packages"`
	}
	if e := json.Unmarshal(body, &index); e != nil {
		return nil, fmt.Errorf("invalid yam v1 index: %w", e)
	}
	best := map[string]YamPackage{}
	for _, p := range index.Packages {
		if !yamV1NameRE.MatchString(p.ID) {
			continue
		}
		v := YamPackage{Registry: "v1", Name: p.ID, Version: limitText(p.Version, 40), Summary: limitText(p.Description, 400), Targets: p.Models, Compatible: true}
		for _, f := range p.Features {
			if arch, ok := strings.CutPrefix(f, "arch."); ok {
				if d.Arch != "" && d.Known && arch != d.Arch {
					v.Compatible, v.Reason = false, "arch"
				}
			} else if len(v.Features) < 8 {
				v.Features = append(v.Features, limitText(f, 40))
			}
		}
		if d.Known && len(p.Models) > 0 && !contains(p.Models, d.ID) && !(d.ID == "plum" && contains(p.Models, "yandexplum")) {
			v.Compatible, v.Reason = false, "target"
		}
		old, seen := best[p.ID]
		if !seen || (v.Compatible && !old.Compatible) || (v.Compatible == old.Compatible && newerLooseVersion(v.Version, old.Version)) {
			best[p.ID] = v
		}
	}
	out := make([]YamPackage, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func newerLooseVersion(a, b string) bool {
	parse := func(v string) []int {
		var out []int
		for _, part := range strings.FieldsFunc(strings.TrimPrefix(v, "v"), func(r rune) bool { return r == '.' || r == '-' }) {
			n := 0
			for _, c := range part {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			out = append(out, n)
		}
		return out
	}
	x, z := parse(a), parse(b)
	for i := 0; i < len(x) || i < len(z); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(z) {
			q = z[i]
		}
		if p != q {
			return p > q
		}
	}
	return a > b
}

func limitText(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

func (y *Yam) refreshInstalled(ctx context.Context) {
	v2 := map[string]string{}
	y.mu.Lock()
	available := y.runtimeLocked("v2").Available
	y.mu.Unlock()
	if available {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, e := y.run(c, y.wrapper(), "list")
		cancel()
		if e == nil {
			v2 = ParseYamList(out)
		}
	}
	v1, v1Runtime := map[string]string{}, YamRuntime{}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if req, e := http.NewRequestWithContext(c, "GET", y.Daemon+"/api/plugins", nil); e == nil {
		if res, e := http.DefaultClient.Do(req); e == nil {
			var list []struct {
				Manifest struct {
					ID      string `json:"id"`
					Version string `json:"version"`
				} `json:"manifest"`
			}
			if res.StatusCode == 200 && json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&list) == nil {
				v1Runtime = YamRuntime{Available: true, Path: y.Daemon}
				for _, p := range list {
					v1[p.Manifest.ID] = p.Manifest.Version
				}
			}
			res.Body.Close()
		}
	}
	y.mu.Lock()
	y.installed["v2"], y.installed["v1"] = v2, v1
	y.registries["v1"].Runtime = v1Runtime
	y.mu.Unlock()
}

// ParseYamList reads `yam list`: name<TAB>version<TAB>target.
func ParseYamList(out string) map[string]string {
	installed := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(out))
	for s.Scan() {
		f := strings.Split(strings.TrimSpace(s.Text()), "\t")
		if len(f) >= 2 && yamV2NameRE.MatchString(f[0]) {
			installed[f[0]] = f[1]
		}
	}
	return installed
}

func (y *Yam) findLocked(registry, name string) (YamPackage, bool) {
	r := y.registries[registry]
	if r == nil {
		return YamPackage{}, false
	}
	for _, p := range r.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return YamPackage{}, false
}

// Start runs one yam operation in the background; the UI follows its job.
func (y *Yam) Start(action, registry, name string) (YamJob, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.busy {
		return YamJob{}, errors.New("another yam operation is still running")
	}
	if registry != "v2" && registry != "v1" {
		return YamJob{}, errors.New("unknown yam registry")
	}
	rt := y.runtimeLocked(registry)
	switch action {
	case "install", "remove":
		if (registry == "v2" && !yamV2NameRE.MatchString(name)) || (registry == "v1" && !yamV1NameRE.MatchString(name)) {
			return YamJob{}, errors.New("invalid yam package name")
		}
		if !rt.Available {
			return YamJob{}, fmt.Errorf("yam %s is not available on this station", registry)
		}
		if action == "install" {
			p, ok := y.findLocked(registry, name)
			if !ok {
				return YamJob{}, errors.New("package is not in the loaded yam registry; refresh the catalog")
			}
			if !p.Compatible {
				return YamJob{}, errors.New("package has no build for this station")
			}
		} else if _, ok := y.installed[registry][name]; !ok {
			return YamJob{}, errors.New("package is not installed")
		}
	case "setup":
		if rt.Available || !rt.CanSetup {
			return YamJob{}, fmt.Errorf("yam %s cannot be installed here", registry)
		}
	default:
		return YamJob{}, errors.New("unknown yam action")
	}
	job := &YamJob{ID: randomID(), Action: action, Registry: registry, Name: name, State: "running", Started: time.Now().UTC()}
	y.jobs = append(y.jobs, job)
	if len(y.jobs) > 8 {
		y.jobs = y.jobs[len(y.jobs)-8:]
	}
	y.busy = true
	go y.execute(job)
	return *job, nil
}

func (y *Yam) execute(job *YamJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out string
	var e error
	switch {
	case job.Action == "setup" && y.setupHook != nil:
		out, e = y.setupHook(ctx, job.Registry)
	case job.Action == "setup" && job.Registry == "v1":
		out, e = y.setupLegacy(ctx)
	case job.Action == "setup":
		out, e = y.setup(ctx)
	case job.Registry == "v2" && job.Action == "install":
		var o string
		o, e = y.run(ctx, y.wrapper(), "registry", "add", "official", YamV2Registry, "--key", YamV2Key)
		out = o
		if e == nil {
			o, e = y.run(ctx, y.wrapper(), "install", "--yes", job.Name)
			out += o
		}
	case job.Registry == "v2":
		out, e = y.run(ctx, y.wrapper(), "remove", job.Name)
	default:
		out, e = y.legacyRequest(ctx, job)
	}
	y.refreshInstalled(ctx)
	y.mu.Lock()
	job.Output, job.Finished, job.State = tailText(out, 8192), time.Now().UTC(), "done"
	if e != nil {
		job.State = "error"
		if !strings.Contains(job.Output, e.Error()) {
			job.Output = strings.TrimSpace(job.Output + "\n" + e.Error())
		}
	}
	y.busy = false
	y.mu.Unlock()
}

func (y *Yam) legacyRequest(ctx context.Context, job *YamJob) (string, error) {
	var req *http.Request
	var e error
	if job.Action == "install" {
		body, _ := json.Marshal(map[string]string{"id": job.Name, "registry": YamV1Index})
		req, e = http.NewRequestWithContext(ctx, "POST", y.Daemon+"/api/plugins/registry", bytes.NewReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, e = http.NewRequestWithContext(ctx, "DELETE", y.Daemon+"/api/plugins/"+job.Name, nil)
	}
	if e != nil {
		return "", e
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	out := strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	if res.StatusCode >= 300 {
		return out, fmt.Errorf("yamd HTTP %d", res.StatusCode)
	}
	return fmt.Sprintf("yamd: %s %s OK", job.Action, job.Name), nil
}

// setup installs the yam v2 runtime the way upstream install.sh does: the
// newest stable yam-v* release, its SHA256SUMS, one platform profile and the
// yam-run wrapper. It never touches /etc or the legacy /data/quazar/yam.
func (y *Yam) setup(ctx context.Context) (string, error) {
	var log strings.Builder
	step := func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
	raw, e := y.get(ctx, yamReleases, 4<<20)
	if e != nil {
		return log.String(), e
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if e = json.Unmarshal(raw, &releases); e != nil {
		return log.String(), e
	}
	asset := "yam-aarch64-unknown-linux-musl"
	if y.Device.Arch == "armv7" {
		asset = "yam-armv7-unknown-linux-musleabihf"
	}
	for _, r := range releases {
		if r.Draft || r.Prerelease || !yamTagRE.MatchString(r.Tag) {
			continue
		}
		var binURL, sumsURL string
		var binSize int64
		for _, a := range r.Assets {
			if !strings.HasPrefix(a.URL, "https://github.com/nyakokitsu/yam/releases/download/") {
				continue
			}
			if a.Name == asset {
				binURL, binSize = a.URL, a.Size
			} else if a.Name == "SHA256SUMS" {
				sumsURL = a.URL
			}
		}
		if binURL == "" || sumsURL == "" {
			continue
		}
		step("release %s, %s", r.Tag, asset)
		sums, e := y.get(ctx, sumsURL, 64<<10)
		if e != nil {
			return log.String(), e
		}
		expected := ""
		for _, line := range strings.Split(string(sums), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset && validHash(strings.ToLower(f[0])) {
				expected = strings.ToLower(f[0])
			}
		}
		if expected == "" {
			return log.String(), errors.New("SHA256SUMS has no entry for " + asset)
		}
		if e := enoughStorage(filepath.Dir(y.Root), binSize); e != nil {
			return log.String(), e
		}
		bin, e := y.get(ctx, binURL, 32<<20)
		if e != nil {
			return log.String(), e
		}
		if Digest(bin) != expected {
			return log.String(), errors.New("yam binary SHA-256 mismatch")
		}
		step("SHA-256 %s OK", expected[:16])
		profile, e := y.get(ctx, yamRawBase+r.Tag+"/platforms/"+y.Device.ID+".yaml", 64<<10)
		if e != nil {
			return log.String(), e
		}
		for _, dir := range []string{"bin", "platforms"} {
			if e = os.MkdirAll(filepath.Join(y.Root, dir), 0755); e != nil {
				return log.String(), e
			}
		}
		wrapper := fmt.Sprintf("#!/bin/sh\nexport YAM_PLATFORM='%s'\nexport YAM_PROFILES='%s/platforms'\nexport YAM_ROOT='%s'\nexec '%s/bin/yam' \"$@\"\n", y.Device.ID, y.Root, y.Root, y.Root)
		for _, f := range []struct {
			path string
			data []byte
			mode os.FileMode
		}{{filepath.Join(y.Root, "platforms", y.Device.ID+".yaml"), profile, 0644}, {filepath.Join(y.Root, "bin", "yam"), bin, 0755}, {y.wrapper(), []byte(wrapper), 0755}} {
			if e = writeAtomic(f.path, f.data, f.mode); e != nil {
				return log.String(), e
			}
		}
		step("installed %s", y.wrapper())
		out, e := y.run(ctx, y.wrapper(), "registry", "add", "official", YamV2Registry, "--key", YamV2Key)
		log.WriteString(out)
		return log.String(), e
	}
	return log.String(), errors.New("no stable yam-v release with " + asset)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".new"
	if e := os.WriteFile(tmp, data, mode); e != nil {
		return e
	}
	if e := os.Chmod(tmp, mode); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

// MaxYamUpload follows the yam archive limit (64 MiB compressed).
const MaxYamUpload = 64 << 20

// StartLocal installs a package file from the browser. Local archives carry
// no registry signature; the UI asks the user to trust the author first.
// .yampkg goes to yam v2 (tar.zst), .yap to the legacy yamd (zip or tar.gz).
func (y *Yam) StartLocal(filename string, data []byte) (YamJob, error) {
	base := filepath.Base(filename)
	var registry string
	switch {
	case strings.HasSuffix(strings.ToLower(base), ".yampkg"):
		if !bytes.HasPrefix(data, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
			return YamJob{}, errors.New("not a .yampkg archive (tar.zst expected)")
		}
		registry = "v2"
	case strings.HasSuffix(strings.ToLower(base), ".yap"):
		if !bytes.HasPrefix(data, []byte("PK")) && !bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			return YamJob{}, errors.New("not a .yap archive (zip or tar.gz expected)")
		}
		registry = "v1"
	default:
		return YamJob{}, errors.New("unknown package type; use .yampkg or .yap")
	}
	if len(data) > MaxYamUpload {
		return YamJob{}, errors.New("package exceeds 64 MiB")
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.busy {
		return YamJob{}, errors.New("another yam operation is still running")
	}
	if !y.runtimeLocked(registry).Available {
		return YamJob{}, fmt.Errorf("yam %s is not available on this station", registry)
	}
	job := &YamJob{ID: randomID(), Action: "install-local", Registry: registry, Name: limitText(base, 120), State: "running", Started: time.Now().UTC()}
	y.jobs = append(y.jobs, job)
	if len(y.jobs) > 8 {
		y.jobs = y.jobs[len(y.jobs)-8:]
	}
	y.busy = true
	go y.executeLocal(job, data)
	return *job, nil
}

func (y *Yam) executeLocal(job *YamJob, data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out string
	e := func() error {
		if job.Registry == "v1" {
			return y.legacyUpload(ctx, job.Name, data, &out)
		}
		dir := filepath.Join(y.Root, ".homebrew-uploads")
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		path := filepath.Join(dir, randomID()+".yampkg")
		if e := os.WriteFile(path, data, 0600); e != nil {
			return e
		}
		defer os.Remove(path)
		o, e := y.run(ctx, y.wrapper(), "install", "--yes", path)
		out = o
		return e
	}()
	y.refreshInstalled(ctx)
	y.mu.Lock()
	job.Output, job.Finished, job.State = tailText(out, 8192), time.Now().UTC(), "done"
	if e != nil {
		job.State = "error"
		if !strings.Contains(job.Output, e.Error()) {
			job.Output = strings.TrimSpace(job.Output + "\n" + e.Error())
		}
	}
	y.busy = false
	y.mu.Unlock()
}

func (y *Yam) legacyUpload(ctx context.Context, name string, data []byte, out *string) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, e := form.CreateFormFile("package", name)
	if e != nil {
		return e
	}
	part.Write(data)
	form.Close()
	req, e := http.NewRequestWithContext(ctx, "POST", y.Daemon+"/api/plugins", &body)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	*out = strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	if res.StatusCode >= 300 {
		return fmt.Errorf("yamd HTTP %d", res.StatusCode)
	}
	*out = "yamd: installed " + name
	return nil
}
