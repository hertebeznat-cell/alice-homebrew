//go:build linux

package homebrew

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// sysfsLayout mirrors the ring layouts the legacy yamd drove through
// /sys/class/leds: <prefix><colour>:ring<N>/brightness.
type sysfsLayout struct {
	id, prefix  string
	base, count int
	// trigger restores the station's own animation; empty means none known.
	trigger string
}

var sysfsLayouts = []sysfsLayout{
	{"micro", "micro:", 1, 3, ""},
	{"midi", "cucumber:", 0, 24, "ledfb-ring"},
	{"plum", "plum:", 1, 3, "led_ring_fb"},
	{"mini2", "mini2:", 1, 6, ""},
}

var sysfsLEDRoot = "/sys/class/leds"

func openNativeRing() (ringBackend, error) {
	for _, l := range sysfsLayouts {
		if _, e := os.Stat(filepath.Join(sysfsLEDRoot, fmt.Sprintf("%sred:ring%d", l.prefix, l.base), "brightness")); e == nil {
			return &sysfsRing{layout: l, last: map[string]int{}}, nil
		}
	}
	if AllowI2CRing {
		return openI2CRing()
	}
	return nil, errors.New("no supported LED ring on this station")
}

type sysfsRing struct {
	layout sysfsLayout
	last   map[string]int
}

func (s *sysfsRing) Layout() RingLayout {
	return RingLayout{ID: s.layout.id, Count: s.layout.count}
}
func (s *sysfsRing) dir(colour string, i int) string {
	return filepath.Join(sysfsLEDRoot, fmt.Sprintf("%s%s:ring%d", s.layout.prefix, colour, i+s.layout.base))
}

// Write touches only values that changed: sysfs writes are syscalls each.
func (s *sysfsRing) Write(rgb []byte) error {
	for i := 0; i < s.layout.count && i*3+2 < len(rgb); i++ {
		for c, colour := range []string{"red", "green", "blue"} {
			path := filepath.Join(s.dir(colour, i), "brightness")
			v := int(rgb[i*3+c])
			if old, ok := s.last[path]; ok && old == v {
				continue
			}
			if e := os.WriteFile(path, []byte(strconv.Itoa(v)), 0); e != nil {
				return e
			}
			s.last[path] = v
		}
	}
	return nil
}

// Release hands the LEDs back to the station animation. A trigger is written
// only when the kernel lists it, as yamd does for Station Lite 2.
func (s *sysfsRing) Release() error {
	s.last = map[string]int{}
	for i := 0; i < s.layout.count; i++ {
		for _, colour := range []string{"red", "green", "blue"} {
			dir := s.dir(colour, i)
			if e := os.WriteFile(filepath.Join(dir, "brightness"), []byte("0"), 0); e != nil {
				return e
			}
			if s.layout.trigger == "" {
				continue
			}
			available, e := os.ReadFile(filepath.Join(dir, "trigger"))
			if e != nil {
				return e
			}
			found := false
			for _, t := range strings.Fields(string(available)) {
				if strings.Trim(t, "[]") == s.layout.trigger {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("station LED trigger %s is unavailable", s.layout.trigger)
			}
			if e = os.WriteFile(filepath.Join(dir, "trigger"), []byte(s.layout.trigger), 0); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *sysfsRing) Close() error { return nil }

// Station Mini 1: LED driver at 0x3c on /dev/i2c-0, 36 PWM channels.
type i2cRing struct {
	fd          int
	initialized bool
}

func openI2CRing() (ringBackend, error) {
	fd, e := syscall.Open("/dev/i2c-0", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x0703, 0x3c); err != 0 {
		syscall.Close(fd)
		return nil, err
	}
	return &i2cRing{fd: fd}, nil
}
func (r *i2cRing) Layout() RingLayout { return mini1Layout }
func (r *i2cRing) writeReg(reg, value byte) error {
	n, err := syscall.Write(r.fd, []byte{reg, value})
	if err == nil && n != 2 {
		return errors.New("short I2C write")
	}
	return err
}
func (r *i2cRing) Write(rgb []byte) error {
	if len(rgb) != ringFrameBytes {
		return errors.New("invalid ring frame")
	}
	if !r.initialized {
		if e := r.writeReg(0, 1); e != nil {
			return e
		}
		for ch := byte(1); ch <= 36; ch++ {
			if e := r.writeReg(0x25+ch, 3); e != nil {
				return e
			}
		}
		r.initialized = true
	}
	// Channels per LED are B,G,R.
	for i := 0; i < 12; i++ {
		for k, v := range []byte{rgb[i*3+2], rgb[i*3+1], rgb[i*3]} {
			if e := r.writeReg(byte(i*3+k+1), v); e != nil {
				return e
			}
		}
	}
	if e := r.writeReg(0x4a, 0); e != nil {
		return e
	}
	return r.writeReg(0x25, 0)
}

// The Mini 1 platformd redraws its own frames; nothing to restore.
func (r *i2cRing) Release() error { return nil }
func (r *i2cRing) Close() error   { return syscall.Close(r.fd) }

func availableBytes(dir string) uint64 {
	var s syscall.Statfs_t
	if syscall.Statfs(dir, &s) != nil {
		return 0
	}
	return s.Bavail * uint64(s.Bsize)
}
