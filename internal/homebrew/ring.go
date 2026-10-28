package homebrew

import (
	"fmt"
	"sync"
	"time"
)

// Plugins always draw the Station Mini 1 frame: 12 RGB LEDs, 36 bytes in
// B,G,R order. LEDs 0-3 sit under the centre logo, 4-11 form the outer ring.
// Other stations receive the outer ring resampled onto their own LEDs.
const ringFrameBytes = 36

// RingLayout describes the LEDs a station really has.
type RingLayout struct {
	ID     string `json:"id"`
	Count  int    `json:"count"`
	Center int    `json:"center"`
}

var mini1Layout = RingLayout{ID: "mini1", Count: 12, Center: 4}

// ringBackend writes device colours (R,G,B per LED) and can hand the LEDs
// back to the station's own animation.
type ringBackend interface {
	Layout() RingLayout
	Write(rgb []byte) error
	Release() error
	Close() error
}

type Ring struct {
	mu        sync.Mutex
	frame     []byte
	pending   []byte
	lastError string
	backend   ringBackend
	layout    RingLayout
	lastDraw  time.Time
	attached  bool
	Hardware  bool
}

// AllowI2CRing enables the Mini 1 I2C driver; main sets it only on that model.
var AllowI2CRing = false

func NewRing(hardware bool) (*Ring, error) {
	r := &Ring{frame: make([]byte, ringFrameBytes), Hardware: hardware, layout: mini1Layout}
	if hardware {
		b, e := openNativeRing()
		if e != nil {
			// A missing driver must not stop the manager: plugins keep
			// running and the UI shows the frame without hardware output.
			r.Hardware = false
			r.lastError = "LED ring unavailable: " + e.Error()
			return r, nil
		}
		r.backend, r.layout = b, b.Layout()
	}
	return r, nil
}

// Layout is the LED arrangement shown in the UI and used for output.
func (r *Ring) Layout() RingLayout { r.mu.Lock(); defer r.mu.Unlock(); return r.layout }

// SetPreviewLayout lets a computer preview draw another station's LEDs.
func (r *Ring) SetPreviewLayout(l RingLayout) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backend == nil && l.Count > 0 {
		r.layout = l
	}
}

func (r *Ring) Publish(frame []byte) {
	if len(frame) != ringFrameBytes {
		return
	}
	r.mu.Lock()
	r.frame = append(r.frame[:0], frame...)
	r.pending = append(r.pending[:0], frame...)
	r.mu.Unlock()
}

// Flush writes the newest frame. When no plugin has drawn for a second, the
// LEDs go back to the station so Alice's own light works again.
func (r *Ring) Flush() {
	r.mu.Lock()
	frame, b := r.pending, r.backend
	r.pending = nil
	now := time.Now()
	release := b != nil && r.attached && frame == nil && now.Sub(r.lastDraw) > time.Second
	if frame != nil && b != nil {
		r.lastDraw, r.attached = now, true
	}
	if release {
		r.attached = false
	}
	layout := r.layout
	r.mu.Unlock()
	var e error
	if len(frame) == ringFrameBytes && b != nil {
		e = b.Write(MapRingFrame(frame, layout))
	} else if release {
		e = b.Release()
	}
	if e != nil {
		r.mu.Lock()
		r.lastError = e.Error()
		r.mu.Unlock()
	}
}

// MapRingFrame converts a plugin frame into R,G,B bytes for each device LED.
func MapRingFrame(frame []byte, l RingLayout) []byte {
	rgb := func(i int) (byte, byte, byte) { return frame[i*3+2], frame[i*3+1], frame[i*3] }
	out := make([]byte, 0, l.Count*3)
	if l.ID == mini1Layout.ID {
		for i := 0; i < 12; i++ {
			r, g, b := rgb(i)
			out = append(out, r, g, b)
		}
		return out
	}
	for j := 0; j < l.Count; j++ {
		r, g, b := rgb(4 + j*8/l.Count)
		out = append(out, r, g, b)
	}
	return out
}

func (r *Ring) Snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	mapped := MapRingFrame(r.frame, r.layout)
	leds := make([]string, 0, r.layout.Count)
	for i := 0; i+2 < len(mapped); i += 3 {
		leds = append(leds, fmt.Sprintf("#%02x%02x%02x", mapped[i], mapped[i+1], mapped[i+2]))
	}
	return map[string]any{"leds": leds, "layout": r.layout, "hardware": r.Hardware, "error": r.lastError}
}
func (r *Ring) Close() {
	r.mu.Lock()
	b := r.backend
	r.mu.Unlock()
	if b != nil {
		b.Release()
		b.Close()
	}
}
