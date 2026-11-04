# AirPlay shared playback: firmware ALSA compatibility

## Reproduction

On a Mini 1 (YNDX-0004), firmware `1.369.4.50.4794692641.20261021`, AirPlay beta.3 accepts the stream and publishes nonzero PCM levels, but no music is audible. Alice's own speech works. Running the same firmware `aplay` locally reproduces silence, nonsensical PCM rates/start thresholds, or `set_params: not enough memory`.

The firmware player has unversioned ALSA imports. Its library exports hidden legacy `ALSA_0.9` APIs alongside current `ALSA_0.9.0rc4` APIs. Some legacy variants return a value instead of filling an output pointer, so binding these variants leaves the player's modern API arguments uninitialized.

## Scope of the workaround

- It applies only to the verified file pair: `aplay` SHA-256 `7ffba196131d8669b04fcd16f7747bfffb1cb29b66881f04129269da47e48ee0`, and `libasound.so.2` SHA-256 `df8f67691cf9a59a803f4e264ad86cd77fcb9e00da1ff4a8c1e33d2e83359f1a`.
- A private copy clears the names of 65 hidden legacy duplicate function symbols. Current exports, executable code, version tables and dmix shared-memory layout remain unchanged. No firmware binary is included in the repository or plugin.
- Only the firmware `aplay` child receives the private `LD_LIBRARY_PATH`. Alice and other processes keep the original library. Direct audio mode is unchanged.
- Unknown file pairs keep the prior shared-mode behavior. Errors while repairing the verified pair are reported rather than silently starting the known broken path.
- The private file is written atomically, mode `0600`, inside the plugin's data directory. An identical copy is reused. The combined ABI/SRC build reserves 5 MiB beyond the receiver for private libraries, atomic replacement and headroom.

## Verification

The compatibility copy with SHA-256 `fa6d683ab80a5dcc6edf240aa4148495d074b847f79e731a5c1599cd8ee84d44` was tested on that Mini. Three generated tones became audible through the original shared `alice_shared` PCM. After a local plugin build used the same copy, the device owner confirmed both AirPlay music and Alice's spoken reply. The receiver binary, firmware player and original library hashes were unchanged. The aplay process mapped the private copy.

This verifies one firmware/file pair, not every Mini revision or firmware. It does not claim verification of reboot, AirPlay 2, multiroom, or other station models.

The ordinary Go suite runs synthetic ELF regression tests without firmware fixtures:

```sh
go test ./plugins/airplay/internal/alsacompat
```

Optional private-file validation additionally reproduces the exact audible-tested copy, checks untouched code sections, private file permissions, reuse, mismatched-player handling, and preservation of original files:

```sh
AIRPLAY_TEST_ALSA=/path/to/original/libasound.so.2 \
AIRPLAY_TEST_APLAY=/path/to/original/aplay \
go test ./plugins/airplay/internal/alsacompat
```

The firmware files are deliberately not checked in. Full service tests and cross-builds still require the existing `receiver-arm64` build artifact as described in the plugin README.
