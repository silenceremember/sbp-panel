package agent

import "testing"

func TestParseContainerState(t *testing.T) {
	state, err := parseContainerState("true|running|2|0\n")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Running || state.Status != "running" || state.RestartCount != 2 || state.ExitCode != 0 {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestParseContainerStateRejectsMalformedOutput(t *testing.T) {
	for _, value := range []string{"", "true|running", "maybe|running|0|0", "true|running|many|0"} {
		if _, err := parseContainerState(value); err == nil {
			t.Fatalf("accepted malformed state %q", value)
		}
	}
}
