//go:build airplay

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"alicehomebrew/plugins/airplay/internal/alsacompat"
)

const sharedALSAConfig = `</usr/share/alsa/alsa.conf>
defaults.pcm.rate_converter "linear"
pcm.alice_shared {
 type plug
 rate_converter "linear"
 slave.pcm "dmixer"
}
`

// Use the firmware's aplay and libasound together. Its dmix shared-memory ABI
// must match Alice's library; mixing it with the embedded modern ALSA is unsafe.
func newSharedPlayback(root string) (*sharedPlayback, error) {
	for _, path := range []string{"/system/workdir/bin/aplay", "/system/vendor/quasar/libs/libasound.so.2", "/usr/share/alsa/alsa.conf"} {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("shared Alice audio requires firmware file %s: %w", path, err)
		}
	}
	config := filepath.Join(root, "shared-alsa.conf")
	compat, err := alsacompat.Prepare(root, "/system/vendor/quasar/libs/libasound.so.2", "/system/workdir/bin/aplay")
	if err != nil {
		return nil, fmt.Errorf("prepare firmware aplay compatibility: %w", err)
	}
	libraryPath := "/system/vendor/quasar/libs:/system/workdir/lib"
	if compat != "" {
		libraryPath = compat + ":" + libraryPath
	}
	if err := os.WriteFile(config, []byte(sharedALSAConfig), 0600); err != nil {
		return nil, err
	}
	p := &sharedPlayback{done: make(chan struct{})}
	p.open = func() (io.WriteCloser, error) {
		// The firmware creates this route during boot. Refuse an exclusive
		// hardware fallback if the shared route is not ready.
		if _, err := os.Stat("/tmp/alsa/dmixer.conf"); err != nil {
			return nil, errors.New("Alice shared mixer is unavailable; use direct mode only if Alice is disabled")
		}
		cmd := exec.Command("/system/workdir/bin/aplay", "-q", "-D", "alice_shared", "-t", "raw", "-f", "S16_LE", "-r", "44100", "-c", "2")
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+libraryPath, "ALSA_CONFIG_PATH="+config)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		if err = cmd.Start(); err != nil {
			stdin.Close()
			return nil, err
		}
		process := &stockPlayer{WriteCloser: stdin, cmd: cmd, done: make(chan struct{})}
		go func() { process.err = cmd.Wait(); close(process.done) }()
		return process, nil
	}
	p.meter = newPCMMeter()
	go p.releaseLoop()
	return p, nil
}

type stockPlayer struct {
	io.WriteCloser
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *stockPlayer) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, fmt.Errorf("firmware audio player stopped: %v", p.err)
	default:
		if pipe, ok := p.WriteCloser.(*os.File); ok {
			_ = pipe.SetWriteDeadline(time.Now().Add(2 * time.Second))
		}
		return p.WriteCloser.Write(b)
	}
}

func (p *stockPlayer) Close() error {
	err := p.WriteCloser.Close()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	return err
}

type sharedPlayback struct {
	mu     sync.Mutex
	open   func() (io.WriteCloser, error)
	sink   io.WriteCloser
	last   time.Time
	done   chan struct{}
	once   sync.Once
	meter  *pcmMeter
	closed bool
}

func (p *sharedPlayback) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	if len(b) == 0 {
		return 0, nil
	}
	if p.sink == nil {
		var err error
		p.sink, err = p.open()
		if err != nil {
			return 0, err
		}
	}
	n, err := p.sink.Write(b)
	p.last = time.Now()
	if p.meter != nil {
		p.meter.add(b[:n])
	}
	return n, err
}

func (p *sharedPlayback) releaseIdle(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sink != nil && now.Sub(p.last) >= 2*time.Second {
		_ = p.sink.Close()
		p.sink = nil
	}
}

func (p *sharedPlayback) releaseLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			p.releaseIdle(now)
		case <-p.done:
			return
		}
	}
}

func (p *sharedPlayback) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		if p.done != nil {
			close(p.done)
		}
		if p.sink != nil {
			_ = p.sink.Close()
			p.sink = nil
		}
		if p.meter != nil {
			p.meter.close()
		}
	})
	return nil
}
