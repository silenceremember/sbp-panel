package agent

import (
	"fmt"
	"testing"
)

func TestParseDockerBytes(t *testing.T) {
	for input, want := range map[string]uint64{"0B": 0, "1.5kB": 1500, "2MiB": 2 << 20, "3 GB": 3_000_000_000} {
		if got := parseDockerBytes(input); got != want {
			t.Fatalf("parseDockerBytes(%q)=%d want %d", input, got, want)
		}
	}
}

func TestUpdateTrafficState(t *testing.T) {
	state := updateTrafficState(trafficMeterState{}, "2026-08", "eth0", 1000, 2000)
	if state.RXBytes != 0 || state.TXBytes != 0 {
		t.Fatalf("initial counters must be a baseline: %+v", state)
	}
	state = updateTrafficState(state, "2026-08", "eth0", 1600, 2900)
	if state.RXBytes != 600 || state.TXBytes != 900 {
		t.Fatalf("unexpected accumulated traffic: %+v", state)
	}
	state = updateTrafficState(state, "2026-08", "eth0", 100, 150)
	if state.RXBytes != 700 || state.TXBytes != 1050 {
		t.Fatalf("counter reset was not accounted for: %+v", state)
	}
	state = updateTrafficState(state, "2026-09", "eth0", 300, 400)
	if state.RXBytes != 0 || state.TXBytes != 0 || state.Month != "2026-09" {
		t.Fatalf("new month was not reset: %+v", state)
	}
}

func TestLimitTrafficMetersBoundsPersistentState(t *testing.T) {
	meters := make(map[string]roomTrafficMeterState, maxTrafficMeterEntries+5)
	for i := range maxTrafficMeterEntries + 5 {
		meters[fmt.Sprintf("device-%05d", i)] = roomTrafficMeterState{}
	}
	limitTrafficMeters(meters)
	if len(meters) != maxTrafficMeterEntries {
		t.Fatalf("meter entries=%d, want %d", len(meters), maxTrafficMeterEntries)
	}
}

func TestBypassTrafficContainerRequiresDedicatedDevice(t *testing.T) {
	match := bypassRoomContainerName.FindStringSubmatch("vpn-panel-bypass-vk-g7-d11")
	if len(match) != 4 || match[1] != "vk" || match[2] != "7" || match[3] != "11" {
		t.Fatalf("dedicated room match = %#v", match)
	}
	for _, name := range []string{
		"vpn-panel-bypass-vk-g7",
		"vpn-panel-bypass-vk-g7-d11-init",
		"vpn-panel-bypass-vk-g0-d11",
	} {
		if bypassRoomContainerName.MatchString(name) {
			t.Fatalf("non-device room %q was accepted for traffic attribution", name)
		}
	}
}

func TestCPUPercent(t *testing.T) {
	got := cpuPercent(cpuCounters{total: 100, idle: 40}, cpuCounters{total: 200, idle: 65})
	if got != 75 {
		t.Fatalf("cpuPercent=%v, want 75", got)
	}
}
