package hosts

import "testing"

func TestTarget(t *testing.T) {
	if got := (Host{Address: "a", User: "u", Port: 22}).Target(); got != "u@a" {
		t.Errorf("Target = %q", got)
	}
	if got := (Host{Address: "a", User: "u", Port: 2222}).Target(); got != "u@a:2222" {
		t.Errorf("Target = %q", got)
	}
}
