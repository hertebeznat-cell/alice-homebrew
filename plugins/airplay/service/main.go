//go:build airplay

// AirPlay audio service. MIT, Alice Homebrew contributors. The embedded
// Shairport/FFmpeg/ALSA/OpenSSL receiver retains its respective licenses.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed receiver-arm64
var receiver []byte

// How often the advertised Wi-Fi address is re-checked.
const networkCheckInterval = 5 * time.Second

type configuration struct {
	Name      string  `json:"name"`
	Password  string  `json:"password"`
	Device    string  `json:"device"`
	MaxVolume float64 `json:"max-volume"`
	Port      int     `json:"port"`
	AudioMode string  `json:"audio-mode"`
}

func configText(c configuration) (string, error) {
	if c.Name == "" || len([]byte(c.Name)) > 48 || strings.ContainsAny(c.Name+c.Password, "\x00\r\n") {
		return "", errors.New("name must be 1-48 UTF-8 bytes; newlines are not allowed")
	}
	if c.Port < 1024 || c.Port > 65535 || c.MaxVolume < -60 || c.MaxVolume > 0 {
		return "", errors.New("invalid port or volume cap")
	}
	if err := validOutput(c.Device); err != nil {
		return "", err
	}
	backend := "stdout"
	if c.AudioMode == "direct" {
		backend = "alsa"
	} else if c.AudioMode != "" && c.AudioMode != "shared" {
		return "", errors.New("choose shared or direct audio mode")
	}
	// JSON quoted strings are compatible with libconfig string escaping here.
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`general = {
 name = %s;
 password = %s;
 port = %d;
 output_backend = %s;
 mdns_backend = "external-avahi";
 alac_decoder = "ffmpeg";
 volume_max_db = %.1f;
 volume_range_db = 60.0;
 ignore_volume_control = "no";
};
alsa = { output_device = %s; output_rate = 48000; output_format = "S32"; disable_standby_mode = "never"; };
stdout = { output_rate = 44100; output_format = "S16"; output_channels = 2; };
`, q(c.Name), q(c.Password), c.Port, q(backend), c.MaxVolume, q("alice_out")), nil
}
func descriptor(b []byte) (string, string, int, []string, error) {
	if len(b) > 16384 {
		return "", "", 0, nil, errors.New("discovery descriptor too long")
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) < 4 || len(lines) > 43 {
		return "", "", 0, nil, errors.New("invalid discovery descriptor")
	}
	port, e := strconv.Atoi(lines[0])
	if e != nil || port < 1024 || port > 65535 {
		return "", "", 0, nil, errors.New("invalid discovery port")
	}
	if lines[2] != "_raop._tcp" || len([]byte(lines[1])) > 63 {
		return "", "", 0, nil, errors.New("invalid RAOP service")
	}
	for _, txt := range lines[3:] {
		if len(txt) > 255 {
			return "", "", 0, nil, errors.New("invalid TXT record")
		}
	}
	return lines[1], lines[2], port, lines[3:], nil
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	b, e := os.ReadFile(os.Getenv("ALICE_CONFIG_PATH"))
	if e != nil {
		return e
	}
	var c configuration
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	text, e := configText(c)
	if e != nil {
		return e
	}
	root := os.Getenv("ALICE_PLUGIN_DATA")
	if root == "" {
		return errors.New("ALICE_PLUGIN_DATA missing")
	}
	// Runtime files stay private to this plugin and are recreated on each start.
	binary := filepath.Join(root, "shairport-sync")
	cfg := filepath.Join(root, "shairport-sync.conf")
	discovery := filepath.Join(root, "discovery.txt")
	if e = os.WriteFile(binary, receiver, 0700); e != nil {
		return e
	}
	if e = os.Chmod(binary, 0700); e != nil {
		return e
	}
	if e = os.WriteFile(cfg, []byte(text), 0600); e != nil {
		return e
	}
	alsaConfig := filepath.Join(root, "alsa.conf")
	pcmList, _ := os.ReadFile("/proc/asound/pcm")
	output, e := directDevice(c.Device, string(pcmList))
	if e != nil && c.AudioMode == "direct" {
		return e
	}
	card, device, _ := strings.Cut(output, ",")
	alsa := fmt.Sprintf("pcm.alice_out { type plug slave.pcm { type hw card %s device %s } }\n", card, device)
	if e = os.WriteFile(alsaConfig, []byte(alsa), 0600); e != nil {
		return e
	}
	_ = os.Remove(discovery)
	cmd := exec.Command(binary, "-c", cfg, "-v")
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "ALICE_MDNS_PATH="+discovery, "ALSA_CONFIG_PATH="+alsaConfig)
	if c.AudioMode == "direct" {
		cmd.Stdout = os.Stdout
	} else {
		shared, err := newSharedPlayback(root)
		if err != nil {
			return err
		}
		defer shared.Close()
		cmd.Stdout = shared
		log.Print("AirPlay shared audio: stock ALSA mixer; Alice services are left running")
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-finished:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var ads advertiser
	defer ads.stop()
	var current []byte
	var checked time.Time
	for {
		select {
		case <-signals:
			return nil
		case e := <-finished:
			if e == nil {
				return errors.New("receiver exited")
			}
			return e
		case now := <-tick.C:
			next, e := os.ReadFile(discovery)
			if e != nil {
				continue
			}
			// Re-check the network every few seconds even when shairport's
			// description is unchanged: Wi-Fi and DHCP change under us.
			if bytes.Equal(next, current) && now.Sub(checked) < networkCheckInterval {
				continue
			}
			name, service, port, txt, e := descriptor(next)
			if e != nil {
				return e
			}
			current, checked = append(current[:0], next...), now
			changed, e := ads.update(name, service, port, txt)
			if e != nil {
				return e
			}
			if changed {
				log.Printf("AirPlay audio ready: %s, TCP %d, mode %s", c.Name, port, modeName(c, output))
			}
		}
	}
}

func modeName(c configuration, output string) string {
	if c.AudioMode == "direct" {
		return "direct plughw:" + output
	}
	return "shared with Alice"
}
