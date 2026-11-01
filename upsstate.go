package apcupsdexporter

import (
	"slices"
	"strconv"
	"strings"
)

// Bits of STATFLAG, from apcupsd's include/defines.h.
const (
	flagOnline     = 0x08
	flagOnBattery  = 0x10
	flagLowBattery = 0x40
	flagCommLost   = 0x100
)

// A upsState is the power state of a UPS.
type upsState struct {
	online, onBattery, lowBattery bool
}

// contactLost needs the bit too, because SHUTTING DOWN replaces COMMLOST in STATUS.
func contactLost(status, statusFlags string) bool {
	if slices.Contains(strings.Fields(status), "COMMLOST") {
		return true
	}

	flags, ok := parseStatusFlags(statusFlags)
	return ok && flags&flagCommLost != 0
}

// parseUPSState decodes STATFLAG, then STATUS, and returns false if neither shows a power state.
func parseUPSState(status, statusFlags string) (upsState, bool) {
	if flags, ok := parseStatusFlags(statusFlags); ok {
		state := upsState{
			online:     flags&flagOnline != 0,
			onBattery:  flags&flagOnBattery != 0,
			lowBattery: flags&flagLowBattery != 0,
		}
		if state.online || state.onBattery {
			return state, true
		}
	}

	// SHUTTING DOWN replaces every other word, so this also covers that state.
	words := strings.Fields(status)
	state := upsState{
		online:     slices.Contains(words, "ONLINE"),
		onBattery:  slices.Contains(words, "ONBATT"),
		lowBattery: slices.Contains(words, "LOWBATT"),
	}

	return state, state.online || state.onBattery
}

func parseStatusFlags(s string) (uint64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}

	flags, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "0x"), 16, 32)
	return flags, err == nil
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
