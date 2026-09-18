package bar

import "testing"

func TestName(t *testing.T) {
	if Name() != "bar" {
		t.Fatalf("Name() = %q, want bar", Name())
	}
}
