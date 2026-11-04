# AirPlay shared playback: sinc resampling on Mini 1

This builds on the included firmware ALSA ABI repair. It addresses muffled
treble after shared playback starts working.

## Scope

Shared playback receives stereo S16_LE at 44100 Hz; firmware dmix runs at
48000 Hz. Replace the explicitly selected linear rate converter with the
firmware's full libsamplerate 0.1.8, `SRC_SINC_MEDIUM_QUALITY`, on the verified
Mini 1 profile only.

The old ALSA library ignores `ALSA_PLUGIN_DIR` and has the obsolete compiled
directory `/lib/linkplay_a98/alsa-lib`. A second player-private copy replaces
only that standalone NUL-terminated string with `/system/workdir/lib`, padded
to preserve offsets. The `/smixer` string, executable code and dmix ABI are
unchanged. Prepend this copy and the full `libsamplerate.so.0` only to the aplay
child's library path, so the reduced vendor libsamplerate cannot shadow it.

Require the successful ABI repair and these SHA-256 fingerprints:

- Repaired ALSA: `fa6d683ab80a5dcc6edf240aa4148495d074b847f79e731a5c1599cd8ee84d44`.
- Full libsamplerate: `015cb1d352cd4423d902a54fc3a875ab1910f7c1e02a3740d49a7c5c612ada8b`.
- Firmware rate module: `6c33f7d5a411ba29010ebc70fb2852cb70dcd5280db451cf622102552e145002`.

Missing/different files retain linear conversion. Preparation/write errors
are reported. Private files are written atomically, mode 0600, and reused when
identical. No firmware binary is distributed or overwritten. Reserve 5 MiB
beyond the receiver for private copies and atomic replacements. Direct mode
and Alice's own library search path are unchanged.

## Digital measurements

Mini 1 / YNDX-0004, firmware `1.369.4.50.4794692641.20261021`.
Generate stereo sine waves of amplitude 0.25, each lasting 0.5 s. Use firmware
aplay with a private ALSA route: plug at 48000 Hz -> file -> null. No speaker or
microphone participates. Use `--period-time=10000 --buffer-time=100000` so the
periods contain integer sample counts. Project the middle 0.25 s of each output
block onto the sine/cosine fundamental at 48000 Hz.

| Frequency | Linear gain | Sinc medium gain |
| --- | ---: | ---: |
| 400 Hz | -0.002 dB | approximately 0 dB |
| 1000 Hz | -0.015 dB | approximately 0 dB |
| 5000 Hz | -0.369 dB | approximately 0 dB |
| 10000 Hz | -1.495 dB | approximately 0 dB |
| 15000 Hz | -3.441 dB | approximately 0 dB |
| 20000 Hz | -6.335 dB | -3.251 dB |

The 48000 -> 48000 control was flat. These measure digital conversion, not the
speaker acoustics. Preliminary measurements using noninteger 125 ms periods
overestimated attenuation through frequency drift; they are excluded.

For three seconds of unpaced file/null output, sinc best took about 9.3 s,
medium 1.0 s and fastest 0.6 s. Best was too slow on this device; fastest lost
about 23 dB at 20 kHz. Medium preserves the tested band through 15 kHz while
processing faster than real time. These are device-specific timings.

## Hardware verification

A local build using the resulting ALSA copy, SHA-256
`7ffd9f3477457825191e0c074aec9fd02aec657e8f6ce3515bafeb1f4d4529a7`,
passed a 14-second realtime silence test through actual shared dmix without
underruns. Over an 8.17 s observation it used about 28.6% of one CPU core.
Actual AirPlay playback mapped this same ALSA copy, the full private
libsamplerate and firmware rate module. The owner confirmed improved music
quality and Alice's audible reply during playback.

The PR adapts that validated library/configuration change to the ABI helper;
its exact output hash is checked using private fixtures. The PR source
adaptation passes unit tests, service tests, vet and a Linux ARM64 cross-build.
Reboot, long-duration load, AirPlay 2 and other station models are unverified.

## Automated checks

Synthetic fixtures cover fingerprint gating, missing files, bounded string
replacement, unchanged inputs, private permissions, reuse and write errors:

```sh
go test ./plugins/airplay/internal/alsacompat
```

Optional private fixtures reproduce the exact listening-tested copy. They
are not required by CI and must not be checked in:

```sh
AIRPLAY_TEST_ALSA=/path/to/original/libasound.so.2 \
AIRPLAY_TEST_APLAY=/path/to/original/aplay \
AIRPLAY_TEST_SAMPLERATE=/path/to/full/libsamplerate.so.0.1.8 \
AIRPLAY_TEST_RATE_MODULE=/path/to/libasound_module_rate_samplerate.so \
go test ./plugins/airplay/internal/alsacompat
```

Service tests and cross-builds require the existing `receiver-arm64` artifact
described in the README. `go test -tags=airplay ./plugins/airplay/service` also
checks beta.4 route selection: unknown firmware retains linear conversion,
while the optional private fixtures verify sinc selection, missing-SRC fallback
and the firmware default PCM without a Mini 1 mixer. Other stations keep the
upstream route and network-discovery implementation.

On macOS, full `go test ./...` retains the same nine pre-existing storage
failures documented in `VALIDATION-alsa-compat.md`; the AirPlay tests pass.
