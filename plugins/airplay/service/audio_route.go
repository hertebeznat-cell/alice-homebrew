//go:build airplay

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Station Mini 1 ships aplay and libasound outside the standard paths and
// builds its shared mixer route at boot. Other stations keep aplay in PATH
// and route their ALSA "default" PCM through the stock mixer themselves.
const (
	mini1Aplay       = "/system/workdir/bin/aplay"
	mini1Libraries   = "/system/vendor/quasar/libs:/system/workdir/lib"
	mini1MixerRoute  = "/tmp/alsa/dmixer.conf"
	firmwareALSAConf = "/usr/share/alsa/alsa.conf"
)

var aplayCandidates = []string{mini1Aplay, "/usr/bin/aplay", "/bin/aplay", "/system/bin/aplay", "/usr/sbin/aplay"}

// sharedRoute plays PCM through the firmware's own aplay and libasound, so the
// stock mixer's shared-memory ABI always matches the one Alice uses.
type sharedRoute struct {
	Player  string
	Device  string
	Env     []string
	Summary string
}

// findSharedRoute inspects the firmware under root ("/" on a speaker).
func findSharedRoute(root, pluginData string) (*sharedRoute, error) {
	exists := func(path string) bool { _, err := os.Stat(filepath.Join(root, path)); return err == nil }
	player := ""
	for _, path := range aplayCandidates {
		if exists(path) {
			player = path
			break
		}
	}
	if player == "" && root == "/" {
		if path, err := exec.LookPath("aplay"); err == nil {
			player = path
		}
	}
	if player == "" {
		return nil, errors.New("this firmware has no aplay, so AirPlay cannot share audio with Alice; " +
			"choose \"Direct output\" in the plugin settings")
	}
	route := &sharedRoute{Player: filepath.Join(root, player), Device: "default"}
	if player == mini1Aplay {
		route.Env = append(route.Env, "LD_LIBRARY_PATH="+mini1Libraries)
	}
	if exists(mini1MixerRoute) && exists(firmwareALSAConf) {
		config := filepath.Join(pluginData, "shared-alsa.conf")
		if err := os.WriteFile(config, []byte(sharedALSAConfig), 0600); err != nil {
			return nil, err
		}
		route.Device = "alice_shared"
		route.Env = append(route.Env, "ALSA_CONFIG_PATH="+config)
		route.Summary = player + " → Alice mixer (dmixer)"
		return route, nil
	}
	// No Mini 1 mixer route: the firmware's default PCM is what Alice itself
	// uses, so leave the firmware ALSA configuration untouched.
	route.Summary = player + " → firmware default PCM"
	return route, nil
}

// playbackDevices lists ALSA playback PCMs as "card,device" from /proc/asound/pcm.
func playbackDevices(pcmList string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(pcmList))
	for scanner.Scan() {
		line := scanner.Text()
		id, _, ok := strings.Cut(line, ":")
		card, device, ok2 := strings.Cut(strings.TrimSpace(id), "-")
		if !ok || !ok2 || !strings.Contains(line, "playback") {
			continue
		}
		c, e1 := strconv.Atoi(card)
		d, e2 := strconv.Atoi(device)
		if e1 == nil && e2 == nil {
			out = append(out, fmt.Sprintf("%d,%d", c, d))
		}
	}
	return out
}

// validOutput accepts "auto" (or empty) and an explicit "plughw:CARD,DEVICE".
func validOutput(setting string) error {
	if setting == "" || setting == "auto" {
		return nil
	}
	value, ok := strings.CutPrefix(setting, "plughw:")
	card, device, ok2 := strings.Cut(value, ",")
	_, e1 := strconv.ParseUint(card, 10, 8)
	_, e2 := strconv.ParseUint(device, 10, 8)
	if !ok || !ok2 || e1 != nil || e2 != nil {
		return errors.New("output must be auto or plughw:CARD,DEVICE")
	}
	return nil
}

// directDevice resolves the configured output to "card,device".
func directDevice(setting, pcmList string) (string, error) {
	if err := validOutput(setting); err != nil {
		return "", err
	}
	if setting != "" && setting != "auto" {
		return strings.TrimPrefix(setting, "plughw:"), nil
	}
	devices := playbackDevices(pcmList)
	if len(devices) == 0 {
		return "", errors.New("no ALSA playback device found in /proc/asound/pcm")
	}
	return devices[0], nil
}
