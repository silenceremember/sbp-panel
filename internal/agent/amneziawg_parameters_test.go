package agent

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func parseAmneziaWGTestSettings(t *testing.T, settings string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(settings), "\n") {
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Fatalf("invalid setting line %q", line)
		}
		values[parts[0]] = parts[1]
	}
	return values
}

func amneziaWGTestInteger(t *testing.T, values map[string]string, key string) int {
	t.Helper()
	value, err := strconv.Atoi(values[key])
	if err != nil {
		t.Fatalf("%s = %q: %v", key, values[key], err)
	}
	return value
}

func TestNewAmneziaWG2Settings(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		settings, err := newAmneziaWG2Settings()
		if err != nil {
			t.Fatal(err)
		}
		values := parseAmneziaWGTestSettings(t, settings.server)
		if len(values) != 11 {
			t.Fatalf("settings = %#v", values)
		}
		jc := amneziaWGTestInteger(t, values, "Jc")
		s1 := amneziaWGTestInteger(t, values, "S1")
		s2 := amneziaWGTestInteger(t, values, "S2")
		s3 := amneziaWGTestInteger(t, values, "S3")
		s4 := amneziaWGTestInteger(t, values, "S4")
		if jc < 4 || jc > 6 || values["Jmin"] != "10" || values["Jmax"] != "50" {
			t.Fatalf("junk settings = %#v", values)
		}
		if s1 < 15 || s1 >= 150 || s2 < 15 || s2 >= 150 || s3 < 0 || s3 >= 64 || s4 < 0 || s4 >= 20 {
			t.Fatalf("padding settings = %d, %d, %d, %d", s1, s2, s3, s4)
		}
		if s1 == s2 || s1 == s3 || s1 == s4 || s2 == s3 || s2 == s4 || s3 == s4 {
			t.Fatalf("padding settings are not distinct: %d, %d, %d, %d", s1, s2, s3, s4)
		}
		if s1+148 == s2+92 || s1+148 == s3+64 || s2+92 == s3+64 {
			t.Fatalf("obfuscated handshake sizes collide: %d, %d, %d", s1+148, s2+92, s3+64)
		}

		previousHigh := uint64(4)
		for index := 1; index <= 4; index++ {
			var low, high uint64
			if _, err := fmt.Sscanf(values[fmt.Sprintf("H%d", index)], "%d-%d", &low, &high); err != nil {
				t.Fatalf("H%d = %q: %v", index, values[fmt.Sprintf("H%d", index)], err)
			}
			if low <= previousHigh || high <= low || high >= uint64(amneziaWGHeaderLimit) {
				t.Fatalf("invalid or overlapping H%d range %d-%d after %d", index, low, high, previousHigh)
			}
			previousHigh = high
		}

		clientValues := parseAmneziaWGTestSettings(t, settings.client)
		if len(clientValues) != 12 || clientValues["I1"] != amneziaWG2DefaultI1 {
			t.Fatalf("client settings = %#v", clientValues)
		}
	}
}
