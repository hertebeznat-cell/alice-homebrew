package homebrew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Legacy yamd (yam v1) is published as yamd-aarch64-linux-musl in the
// v0.1.x releases. Its installer puts it in /data/quazar/yam and starts it
// from /etc/init.d/S99yam; Homebrew does the same on the station.
var yamLegacyTagRE = regexp.MustCompile(`^v0\.1\.[0-9]+$`)

const yamLegacyAsset = "yamd-aarch64-linux-musl"

func enoughStorage(dir string, need int64) error {
	d := diskUsage(dir)
	required := uint64(need) + StorageReserve
	if d.Available && d.AvailableBytes < required {
		return fmt.Errorf("not enough storage: %d MiB free, %d MiB required", d.AvailableBytes>>20, required>>20)
	}
	return nil
}

type yamRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// verifiedDownload fetches an asset and checks it against SHA256SUMS of the
// same release.
func (y *Yam) verifiedDownload(ctx context.Context, sumsURL, url, name string, limit int64) ([]byte, error) {
	sums, e := y.get(ctx, sumsURL, 64<<10)
	if e != nil {
		return nil, e
	}
	expected := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && validHash(strings.ToLower(f[0])) {
			expected = strings.ToLower(f[0])
		}
	}
	if expected == "" {
		return nil, errors.New("SHA256SUMS has no entry for " + name)
	}
	b, e := y.get(ctx, url, limit)
	if e != nil {
		return nil, e
	}
	if Digest(b) != expected {
		return nil, errors.New(name + " SHA-256 mismatch")
	}
	return b, nil
}

func (y *Yam) setupLegacy(ctx context.Context) (string, error) {
	var log strings.Builder
	step := func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
	raw, e := y.get(ctx, yamReleases, 4<<20)
	if e != nil {
		return log.String(), e
	}
	var releases []yamRelease
	if e = json.Unmarshal(raw, &releases); e != nil {
		return log.String(), e
	}
	for _, r := range releases {
		if r.Draft || r.Prerelease || !yamLegacyTagRE.MatchString(r.Tag) {
			continue
		}
		var binURL, sumsURL string
		var size int64
		prefix := "https://github.com/nyakokitsu/yam/releases/download/" + r.Tag + "/"
		for _, a := range r.Assets {
			if a.URL == prefix+yamLegacyAsset {
				binURL, size = a.URL, a.Size
			} else if a.URL == prefix+"SHA256SUMS" {
				sumsURL = a.URL
			}
		}
		if binURL == "" || sumsURL == "" {
			continue
		}
		step("release %s, %s", r.Tag, yamLegacyAsset)
		if e = enoughStorage(filepath.Dir(y.LegacyRoot), size); e != nil {
			return log.String(), e
		}
		bin, e := y.verifiedDownload(ctx, sumsURL, binURL, yamLegacyAsset, 32<<20)
		if e != nil {
			return log.String(), e
		}
		step("SHA-256 OK")
		for _, dir := range []string{"bin", "data"} {
			if e = os.MkdirAll(filepath.Join(y.LegacyRoot, dir), 0755); e != nil {
				return log.String(), e
			}
		}
		if e = writeAtomic(filepath.Join(y.LegacyRoot, "bin", "yamd"), bin, 0755); e != nil {
			return log.String(), e
		}
		out, e := y.run(ctx, "/bin/sh", "-c", legacyStartScript(y.LegacyRoot))
		log.WriteString(out)
		if e != nil {
			return log.String(), e
		}
		for i := 0; i < 20; i++ {
			y.refreshInstalled(ctx)
			y.mu.Lock()
			ok := y.registries["v1"].Runtime.Available
			y.mu.Unlock()
			if ok {
				step("yamd is running on port 8099")
				return log.String(), nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return log.String(), errors.New("yamd did not start; see the log")
	}
	return log.String(), errors.New("no stable yam v0.1 release with " + yamLegacyAsset)
}

// legacyStartScript starts yamd and installs the upstream boot hook. An
// S99yam that belongs to something else (yam v2 on Station Lite) is kept.
func legacyStartScript(root string) string {
	return fmt.Sprintf(`set -e
prefix='%[1]s'
pidfile="$prefix/yamd.pid"
if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then kill "$(cat "$pidfile")"; sleep 1; fi
rm -f "$pidfile"
start-stop-daemon --start --background --make-pidfile --pidfile "$pidfile" --startas "$prefix/bin/yamd" -- --data-dir "$prefix/data"
hook=/etc/init.d/S99yam
if [ -e "$hook" ] && ! grep -q "/data/quazar/yam" "$hook"; then
  echo "kept existing $hook"
  exit 0
fi
mount -o remount,rw /
trap 'mount -o remount,ro /' EXIT
cat > "$hook" <<'HOOK'
#!/bin/sh
case "${1:-}" in
  start)
    prefix=/data/quazar/yam
    pidfile="$prefix/yamd.pid"
    [ -x "$prefix/bin/yamd" ] || exit 0
    mkdir -p "$prefix/data"
    if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then exit 0; fi
    rm -f "$pidfile"
    start-stop-daemon --start --background --make-pidfile --pidfile "$pidfile" --startas "$prefix/bin/yamd" -- --data-dir "$prefix/data"
    ;;
  stop)
    prefix=/data/quazar/yam
    [ -f "$prefix/yamd.pid" ] && kill "$(cat "$prefix/yamd.pid")" 2>/dev/null || true
    rm -f "$prefix/yamd.pid"
    ;;
esac
HOOK
chmod 0755 "$hook"
echo "boot hook $hook installed"
`, root)
}

// AutoSetup installs yam v2 and then yamd once, right after Homebrew 6
// first starts on a station. The record keeps a removed yam from coming back;
// failures stay visible as jobs and can be retried from the store.
func (y *Yam) AutoSetup(ctx context.Context, record string) {
	done := map[string]string{}
	if b, e := os.ReadFile(record); e == nil {
		_ = json.Unmarshal(b, &done)
	}
	save := func() { _ = atomicJSON(record, done) }
	for _, reg := range []string{"v2", "v1"} {
		if done[reg] != "" {
			continue
		}
		y.mu.Lock()
		rt := y.runtimeLocked(reg)
		y.mu.Unlock()
		if rt.Reason == "preview" {
			return
		}
		if rt.Available || !rt.CanSetup {
			done[reg] = map[bool]string{true: "present", false: "unsupported"}[rt.Available]
			save()
			continue
		}
		var job YamJob
		var e error
		for i := 0; i < 600; i++ {
			if job, e = y.Start("setup", reg, ""); e == nil || !strings.Contains(e.Error(), "still running") {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		if e != nil {
			done[reg] = "error: " + e.Error()
			save()
			continue
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			if state := y.jobState(job.ID); state != "running" {
				done[reg] = state
				break
			}
		}
		save()
	}
}

func (y *Yam) jobState(id string) string {
	y.mu.Lock()
	defer y.mu.Unlock()
	for _, j := range y.jobs {
		if j.ID == id {
			return j.State
		}
	}
	return "done"
}
