package homebrew

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const UpdateInterval = 5 * time.Hour
const updateRepository = "hertebeznat-cell/alice-homebrew"
const releaseAPI = "https://api.github.com/repos/" + updateRepository + "/releases/latest"
const maxUpdateBinary = 32 << 20

type UpdateStatus struct {
	Enabled       bool      `json:"enabled"`
	Supported     bool      `json:"supported"`
	Current       string    `json:"current_version"`
	Latest        string    `json:"latest_version,omitempty"`
	State         string    `json:"state"`
	LastCheck     time.Time `json:"last_check,omitempty"`
	NextCheck     time.Time `json:"next_check,omitempty"`
	LastUpdate    time.Time `json:"last_update,omitempty"`
	Error         string    `json:"error,omitempty"`
	FailedVersion string    `json:"failed_version,omitempty"`
	IntervalHours int       `json:"interval_hours"`
}
type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}
type managerRelease struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type Updater struct {
	mu                      sync.Mutex
	dir, executable, listen string
	args                    []string
	pid                     int
	status                  UpdateStatus
	busy                    bool
	client                  *http.Client
	now                     func() time.Time
	launch                  func(string, string) error
}

func NewUpdater(dir, listen string, managed bool) (*Updater, error) {
	absolute, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	executable, e := os.Executable()
	if e != nil {
		return nil, e
	}
	// Keep /data's launch spelling: Mini 1 resolves it to /bkupgrade/data,
	// while the init hook matches the original argv[0] when checking its PID.
	if launch, err := filepath.Abs(os.Args[0]); err == nil {
		a, ea := os.Stat(launch)
		b, eb := os.Stat(executable)
		if ea == nil && eb == nil && os.SameFile(a, b) {
			executable = launch
		}
	}
	u := &Updater{dir: absolute, executable: executable, listen: listen, args: append([]string{}, os.Args[1:]...), pid: os.Getpid(), now: time.Now, launch: launchUpdateHelper}
	u.status = UpdateStatus{Enabled: true, Supported: runtime.GOOS == "linux" && managed, Current: Version, State: "idle", IntervalHours: 5}
	u.loadLocked()
	u.status.Supported = runtime.GOOS == "linux" && managed
	u.status.Current = Version
	u.status.IntervalHours = 5
	u.client = &http.Client{Transport: trustedHTTPTransport(), Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || !trustedUpdateURL(req.URL) {
			return errors.New("update redirect left GitHub HTTPS")
		}
		return nil
	}}
	if _, err := os.Stat(filepath.Join(u.dir, "manager-update.json")); os.IsNotExist(err) && (u.status.State == "checking" || u.status.State == "downloading" || u.status.State == "restarting") {
		u.status.State = "idle"
		u.status.Error = "previous check was interrupted"
	}
	if u.status.Supported && u.status.NextCheck.IsZero() {
		u.status.NextCheck = time.Now().UTC().Add(15 * time.Second)
	}
	if e = u.saveLocked(); e != nil {
		return nil, e
	}
	return u, nil
}
func trustedUpdateURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func (u *Updater) loadLocked() {
	b, e := os.ReadFile(filepath.Join(u.dir, "updates.json"))
	if e != nil {
		return
	}
	var saved UpdateStatus
	if decode(b, &saved) != nil {
		return
	}
	supported, current := u.status.Supported, u.status.Current
	u.status = saved
	u.status.Supported = supported
	u.status.Current = current
	u.status.IntervalHours = 5
}
func (u *Updater) saveLocked() error {
	return atomicJSON(filepath.Join(u.dir, "updates.json"), u.status)
}
func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.busy {
		u.loadLocked()
	}
	return u.status
}
func (u *Updater) SetEnabled(enabled bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return errors.New("an update check is in progress")
	}
	u.loadLocked()
	old := u.status
	u.status.Enabled = enabled
	if enabled && u.status.NextCheck.IsZero() {
		u.status.NextCheck = u.now()
	}
	if e := u.saveLocked(); e != nil {
		u.status = old
		return e
	}
	return nil
}
func (u *Updater) Run(ctx context.Context) {
	// Persisted NextCheck survives reboots and updates. Check a fresh installation
	// after a short startup grace period, then never more often than five hours.
	startup := time.NewTimer(15 * time.Second)
	defer startup.Stop()
	select {
	case <-ctx.Done():
		return
	case <-startup.C:
	}
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		status := u.Status()
		if status.Supported && status.Enabled && (status.NextCheck.IsZero() || !u.now().Before(status.NextCheck)) {
			_, _ = u.Check(ctx, false)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func parseStableVersion(s string) ([3]uint64, error) {
	var v [3]uint64
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, errors.New("release is not a stable semantic version")
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 || (len(p) > 1 && p[0] == '0') {
			return v, errors.New("invalid version")
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, errors.New("invalid version")
			}
		}
		n, e := strconv.ParseUint(p, 10, 32)
		if e != nil {
			return v, e
		}
		v[i] = n
	}
	return v, nil
}
func newerVersion(candidate, current string) bool {
	a, e := parseStableVersion(candidate)
	if e != nil {
		return false
	}
	b, e := parseStableVersion(current)
	if e != nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
func (u *Updater) get(ctx context.Context, address string, limit int64) (io.ReadCloser, error) {
	parsed, e := url.Parse(address)
	if e != nil || !trustedUpdateURL(parsed) {
		return nil, errors.New("untrusted update URL")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "AliceHomebrew/"+Version)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, e := u.client.Do(req)
	if e != nil {
		return nil, e
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		response.Body.Close()
		return nil, errors.New("update response exceeds size limit")
	}
	return response.Body, nil
}
func selectUpdateAsset(release managerRelease, arch string) (releaseAsset, error) {
	name := "alice-homebrew-linux-" + arch
	for _, a := range release.Assets {
		if a.Name != name {
			continue
		}
		expected := "https://github.com/" + updateRepository + "/releases/download/" + release.Tag + "/" + name
		digest := strings.TrimPrefix(a.Digest, "sha256:")
		if a.URL != expected || !strings.HasPrefix(a.Digest, "sha256:") || !validHash(digest) || a.Size < 64 || a.Size > maxUpdateBinary || a.State != "uploaded" {
			return releaseAsset{}, errors.New("release binary URL, digest or size is invalid")
		}
		return a, nil
	}
	return releaseAsset{}, errors.New("release does not contain the manager binary for this architecture")
}
func validateUpdateBinary(path, arch string) error {
	f, e := elf.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	machine, class := elf.EM_AARCH64, elf.ELFCLASS64
	switch arch {
	case "arm64":
	case "amd64":
		machine = elf.EM_X86_64
	case "arm":
		// 32-bit stations (Station Street, JBL Link Music).
		machine, class = elf.EM_ARM, elf.ELFCLASS32
	default:
		return errors.New("unsupported update architecture")
	}
	if f.Class != class || f.Data != elf.ELFDATA2LSB || f.Machine != machine || f.Type != elf.ET_EXEC {
		return errors.New("manager update is not a matching static Linux ELF")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return errors.New("manager update needs an external loader")
		}
	}
	return nil
}
func (u *Updater) Check(ctx context.Context, manual bool) (result UpdateStatus, err error) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return u.Status(), errors.New("an update check is already in progress")
	}
	u.loadLocked()
	if _, e := os.Stat(filepath.Join(u.dir, "manager-update.json")); e == nil {
		u.mu.Unlock()
		return u.Status(), errors.New("an update transaction is in progress")
	}
	if !manual && !u.status.Enabled {
		result = u.status
		u.mu.Unlock()
		return result, nil
	}
	u.busy = true
	u.status.State = "checking"
	u.status.Error = ""
	u.status.LastCheck = u.now().UTC()
	u.status.NextCheck = u.status.LastCheck.Add(UpdateInterval)
	if e := u.saveLocked(); e != nil {
		u.busy = false
		u.mu.Unlock()
		return u.Status(), e
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.busy = false
		if err != nil {
			u.status.State = "error"
			u.status.Error = err.Error()
		}
		if e := u.saveLocked(); err == nil && e != nil {
			err = e
		}
		result = u.status
	}()
	body, e := u.get(ctx, releaseAPI, 2<<20)
	if e != nil {
		return result, e
	}
	raw, e := io.ReadAll(io.LimitReader(body, (2<<20)+1))
	body.Close()
	if e != nil {
		return result, e
	}
	if len(raw) > 2<<20 {
		return result, errors.New("release metadata too large")
	}
	var release managerRelease
	if e = json.Unmarshal(raw, &release); e != nil {
		return result, e
	}
	if release.Draft || release.Prerelease {
		return result, errors.New("refusing a draft or prerelease manager")
	}
	if _, e = parseStableVersion(release.Tag); e != nil {
		return result, e
	}
	latest := strings.TrimPrefix(release.Tag, "v")
	u.mu.Lock()
	u.status.Latest = latest
	supported, failed := u.status.Supported, u.status.FailedVersion
	u.mu.Unlock()
	if !newerVersion(latest, Version) {
		u.mu.Lock()
		u.status.State = "up-to-date"
		u.mu.Unlock()
		return result, nil
	}
	if !manual && failed == latest {
		u.mu.Lock()
		u.status.State = "rolled-back"
		u.status.Error = "this version previously failed startup; waiting for a newer release"
		u.mu.Unlock()
		return result, nil
	}
	if !supported {
		u.mu.Lock()
		u.status.State = "available"
		u.mu.Unlock()
		return result, nil
	}
	asset, e := selectUpdateAsset(release, runtime.GOARCH)
	if e != nil {
		return result, e
	}
	disk := diskUsage(filepath.Dir(u.executable))
	if !disk.Available || disk.AvailableBytes < uint64(asset.Size)+StorageReserve+fileBytes(filepath.Join(u.dir, "state.json")) {
		return result, errors.New("not enough free storage for manager update and rollback reserve")
	}
	u.mu.Lock()
	u.status.State = "downloading"
	u.saveLocked()
	u.mu.Unlock()
	next := u.executable + ".next"
	if _, e = os.Lstat(next); e == nil {
		if !regularUpdateFile(next) {
			return result, errors.New("staged manager is not a regular file")
		}
		// A verified download can be left here if power was lost before the
		// journal was written. It is an updater-owned file, never plugin data.
		if e = os.Remove(next); e != nil {
			return result, e
		}
	}
	if e = u.downloadBinary(ctx, asset, next); e != nil {
		return result, e
	}
	prepared := false
	defer func() {
		if !prepared {
			os.Remove(next)
		}
	}()
	if e = validateUpdateBinary(next, runtime.GOARCH); e != nil {
		return result, e
	}
	versionContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	actual, e := exec.CommandContext(versionContext, next, "version").Output()
	if e != nil || strings.TrimSpace(string(actual)) != latest {
		return result, errors.New("downloaded manager failed its version self-check")
	}
	health, e := localHealthURL(u.listen)
	if e != nil {
		return result, e
	}
	plan := UpdatePlan{Target: u.executable, Data: u.dir, Args: u.args, PID: u.pid, Version: latest, PreviousVersion: Version, SHA256: strings.TrimPrefix(asset.Digest, "sha256:"), HealthURL: health, Created: u.now().UTC()}
	plan.ProcessStart = processIdentity(u.pid)
	plan.Phase = "prepared"
	previous, e := os.ReadFile(u.executable)
	if e != nil {
		return result, e
	}
	plan.PreviousSHA256 = Digest(previous)
	if plan.ProcessStart == "" {
		return result, errors.New("cannot identify the managed process")
	}
	planPath := filepath.Join(u.dir, "manager-update.json")
	if e = atomicJSON(planPath, plan); e != nil {
		return result, e
	}
	u.mu.Lock()
	u.status.State = "restarting"
	e = u.saveLocked()
	u.mu.Unlock()
	if e != nil {
		os.Remove(planPath)
		return result, e
	}
	if e = u.launch(u.executable, planPath); e != nil {
		os.Remove(planPath)
		return result, e
	}
	prepared = true
	return result, nil
}
func (u *Updater) downloadBinary(ctx context.Context, asset releaseAsset, path string) error {
	body, e := u.get(ctx, asset.URL, maxUpdateBinary)
	if e != nil {
		return e
	}
	defer body.Close()
	partial := path + ".download"
	if _, e := os.Lstat(partial); e == nil {
		if !regularUpdateFile(partial) {
			return errors.New("partial manager download is not a regular file")
		}
		if e = os.Remove(partial); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if e != nil {
		return e
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			os.Remove(partial)
		}
	}()
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(body, asset.Size+1))
	if e != nil {
		return e
	}
	if n != asset.Size || hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(asset.Digest, "sha256:") {
		return errors.New("manager download size or SHA-256 mismatch")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(partial, path); e != nil {
		return e
	}
	complete = true
	return nil
}
func regularUpdateFile(path string) bool {
	s, e := os.Lstat(path)
	return e == nil && s.Mode().IsRegular()
}
func localHealthURL(listen string) (string, error) {
	_, port, e := net.SplitHostPort(listen)
	if e != nil {
		return "", e
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 {
		return "", errors.New("managed service needs a fixed TCP port")
	}
	return "http://127.0.0.1:" + port + "/api/about", nil
}
