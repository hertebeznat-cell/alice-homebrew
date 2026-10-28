//go:build linux

package homebrew

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSysfsRingWriteAndRelease(t *testing.T) {
	root := t.TempDir()
	old := sysfsLEDRoot
	sysfsLEDRoot = root
	defer func() { sysfsLEDRoot = old }()
	for i := 0; i < 24; i++ {
		for _, c := range []string{"red", "green", "blue"} {
			dir := filepath.Join(root, "cucumber:"+c+":ring"+strconv.Itoa(i))
			os.MkdirAll(dir, 0755)
			os.WriteFile(filepath.Join(dir, "brightness"), []byte("0"), 0644)
			os.WriteFile(filepath.Join(dir, "trigger"), []byte("none [ledfb-ring] timer"), 0644)
		}
	}
	b, e := openNativeRing()
	if e != nil || b.Layout().Count != 24 {
		t.Fatalf("midi layout not found: %v", e)
	}
	r := &Ring{frame: make([]byte, 36), backend: b, layout: b.Layout(), Hardware: true}
	frame := make([]byte, 36)
	frame[4*3+2] = 77 // first outer LED red
	r.Publish(frame)
	r.Flush()
	got, _ := os.ReadFile(filepath.Join(root, "cucumber:red:ring0", "brightness"))
	if string(got) != "77" {
		t.Fatalf("red ring0 = %q", got)
	}
	trigger := filepath.Join(root, "cucumber:red:ring0", "trigger")
	os.WriteFile(trigger, []byte("none ledfb-ring timer"), 0644)
	r.mu.Lock()
	r.lastDraw = time.Now().Add(-2 * time.Second)
	r.mu.Unlock()
	r.Flush()
	if got, _ = os.ReadFile(trigger); string(got) != "ledfb-ring" {
		t.Fatalf("station animation not restored: %q", got)
	}
	if got, _ = os.ReadFile(filepath.Join(root, "cucumber:red:ring0", "brightness")); string(got) != "0" {
		t.Fatalf("brightness not cleared: %q", got)
	}
}
