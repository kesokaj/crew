package version

import "testing"

func TestIsDev(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"0.0.0-dev", true},
		{"0.0.0-dev.988fe3a", true},
		{"1.4.0", false},
	} {
		Version = tc.v
		if got := IsDev(); got != tc.want {
			t.Errorf("IsDev() with %q = %v, want %v", tc.v, got, tc.want)
		}
	}
}
