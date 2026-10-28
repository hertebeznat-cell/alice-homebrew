package homebrew

import (
	"bytes"
	"testing"
)

func TestMapRingFrame(t *testing.T) {
	frame := make([]byte, ringFrameBytes)
	for i := 0; i < 12; i++ {
		frame[i*3], frame[i*3+1], frame[i*3+2] = byte(i), byte(100+i), byte(200+i) // B,G,R
	}
	mini := MapRingFrame(frame, mini1Layout)
	if len(mini) != 36 || mini[0] != 200 || mini[1] != 100 || mini[2] != 0 {
		t.Fatalf("mini1 must keep all 12 LEDs as R,G,B: %v", mini[:3])
	}
	midi := MapRingFrame(frame, RingLayout{ID: "midi", Count: 24})
	if len(midi) != 72 || !bytes.Equal(midi[:9], []byte{204, 104, 4, 204, 104, 4, 204, 104, 4}) || midi[69] != 211 {
		t.Fatalf("midi must spread the 8 outer LEDs over 24: %v", midi[:9])
	}
	lite := MapRingFrame(frame, RingLayout{ID: "micro", Count: 3})
	if len(lite) != 9 || lite[0] != 204 || lite[3] != 206 || lite[6] != 209 {
		t.Fatalf("lite: %v", lite)
	}
	r, _ := NewRing(false)
	r.SetPreviewLayout(RingLayout{ID: "mini2", Count: 6})
	r.Publish(frame)
	if leds := r.Snapshot()["leds"].([]string); len(leds) != 6 || leds[0] != "#cc6804" {
		t.Fatalf("snapshot: %v", leds)
	}
}
