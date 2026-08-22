package agent

import "testing"

func TestRouteSourceAddress(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"1.1.1.1 via 198.51.100.1 dev eth0 src 198.51.100.42 uid 0", "198.51.100.42"},
		{"local 1.1.1.1 dev lo src 127.0.0.1", ""},
		{"1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.2", ""},
		{"1.1.1.1 via 10.0.0.1 dev eth0", ""},
	} {
		if got := routeSourceAddress(test.input); got != test.want {
			t.Fatalf("routeSourceAddress(%q)=%q, want %q", test.input, got, test.want)
		}
	}
}
