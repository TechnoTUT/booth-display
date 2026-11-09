package ndi

import "testing"

func TestNames(t *testing.T) {
	if got := StreamName("HOST (OBS Out)"); got != "OBS Out" {
		t.Errorf("StreamName = %q", got)
	}
	if got := HostName("HOST (OBS Out)"); got != "HOST" {
		t.Errorf("HostName = %q", got)
	}
	if got := StreamName("plain"); got != "" {
		t.Errorf("StreamName(plain) = %q", got)
	}
	list := []Source{{Name: "A (cam 10)"}, {Name: "B (cam 1)"}}
	if s, ok := matchSource(list, "cam 1"); !ok || s.Name != "B (cam 1)" {
		t.Errorf("exact stream-name match preferred over substring, got %q %v", s.Name, ok)
	}
	if s, ok := matchSource(list, "A ("); !ok || s.Name != "A (cam 10)" {
		t.Errorf("substring match failed: %q %v", s.Name, ok)
	}
}

// TestLoad needs the NDI runtime; it is skipped when libndi is not available.
func TestLoad(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	t.Logf("libndi %s", Version())
}
