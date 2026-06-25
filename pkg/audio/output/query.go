// ABOUTME: Capability probe for malgo playback devices (native rates and bit-depth)
// ABOUTME: Used by Player to build the advertised SupportedFormats list before handshake
package output

import (
	"fmt"
	"log"
	"sort"

	"github.com/gen2brain/malgo"
)

// QueryDeviceCapabilities returns the native sample rates (sorted ascending)
// and highest bit depth the named playback device's malgo (miniaudio) backend
// reports as natively supported. deviceName matches the same way
// ListPlaybackDevices and Open accept it; an empty string selects the platform
// default.
//
// Best-effort. When the backend reports zero native formats — some devices
// don't, especially on cold-start Windows / Pulse — this returns (nil, 0, nil)
// so the caller can fall back to the hardcoded format list. On Linux/ALSA the
// answer can also be optimistic, because miniaudio reports what the driver
// claims to accept, and ALSA layers software resampling under formats the
// underlying hardware (e.g. bcm2835 onboard headphones) can't actually sustain.
// The user-facing override knob exists exactly for that case.
//
// Does NOT InitDevice. Cheaper than opening the device, but the trade-off is
// that the answer is a best-guess from miniaudio rather than ground truth.
func QueryDeviceCapabilities(deviceName string, shareMode ShareMode) (nativeRates []int, maxBitDepth int, err error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("init malgo context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	infos, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return nil, 0, fmt.Errorf("enumerate playback devices: %w", err)
	}

	catalog := make([]PlaybackDevice, 0, len(infos))
	for _, info := range infos {
		catalog = append(catalog, PlaybackDevice{
			Name:      info.Name(),
			IsDefault: info.IsDefault != 0,
			ID:        info.ID,
		})
	}

	chosen, err := matchDevice(catalog, deviceName)
	if err != nil {
		return nil, 0, err
	}
	if chosen == nil {
		// No devices at all. Treat as "no native formats" — no audio output is
		// going to happen anyway, so the caller's handshake will fail for
		// unrelated reasons.
		return nil, 0, nil
	}

	log.Printf("Probing device capabilities: %q (mode=%s)", chosen.Name, shareModeName(shareMode))
	detail, err := ctx.DeviceInfoEx(malgo.Playback, chosen.ID, malgo.ShareMode(shareMode))
	if err != nil {
		return nil, 0, fmt.Errorf("query device info for %q: %w", chosen.Name, err)
	}

	if len(detail.Formats) == 0 {
		log.Printf("Device %q reported no native formats", chosen.Name)
		return nil, 0, nil
	}

	sort.Slice(detail.Formats, func(i, j int) bool {
		return detail.Formats[i].SampleRate < detail.Formats[j].SampleRate
	})

	for _, f := range detail.Formats {
		log.Printf("  format: %s %dHz", formatName(f.Format), f.SampleRate)
	}

	_, maxDepth := capsFromFormats(detail.Formats)
	rates := nativeRatesFromFormats(detail.Formats)
	log.Printf("Device capability result: rates=%v max=%d-bit", rates, maxDepth)
	return rates, maxDepth, nil
}

// nativeRatesFromFormats extracts a sorted, deduplicated list of sample rates
// from a DeviceInfo native-format list, skipping unknown/unsupported formats.
func nativeRatesFromFormats(formats []malgo.DataFormat) []int {
	seen := make(map[uint32]struct{})
	rates := make([]int, 0, len(formats))
	for _, f := range formats {
		if formatBits(f.Format) == 0 {
			continue
		}
		if _, dup := seen[f.SampleRate]; dup {
			continue
		}
		seen[f.SampleRate] = struct{}{}
		rates = append(rates, int(f.SampleRate))
	}
	// formats is already sorted ascending by SampleRate from the caller
	return rates
}


// capsFromFormats walks a DeviceInfo's native-format list and returns the
// highest sample rate and bit depth observed. Formats with unknown bit
// representations (FormatU8, FormatUnknown) are ignored — we'd rather report
// a lower cap than advertise rates only achievable in unsupported formats.
//
// Pure helper so the cgo-bound QueryDeviceCapabilities doesn't need test
// coverage of its own — capsFromFormats covers the interesting logic.
func capsFromFormats(formats []malgo.DataFormat) (maxSampleRate, maxBitDepth int) {
	for _, f := range formats {
		bits := formatBits(f.Format)
		if bits == 0 {
			continue
		}
		if int(f.SampleRate) > maxSampleRate {
			maxSampleRate = int(f.SampleRate)
		}
		if bits > maxBitDepth {
			maxBitDepth = bits
		}
	}
	return maxSampleRate, maxBitDepth
}

// formatBits returns the linear bit count for a malgo FormatType.
// 0 means unknown/unsupported and the caller should ignore the entry.
func formatBits(f malgo.FormatType) int {
	switch f {
	case malgo.FormatS16:
		return 16
	case malgo.FormatS24:
		return 24
	case malgo.FormatS32:
		return 32
	case malgo.FormatF32:
		// 32-bit float carries the same dynamic range as S32 for our
		// purposes — both clear our 24-bit advertised ceiling.
		return 32
	default:
		return 0
	}
}
