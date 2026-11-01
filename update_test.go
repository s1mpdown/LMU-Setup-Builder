package main

import "testing"

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		remote, local string
		want          bool
	}{
		{"v1.3.0", "1.2.0", true},
		{"v1.2.0", "1.2.0", false},
		{"v1.2.1", "1.2.0", true},
		{"v1.1.9", "1.2.0", false},
		{"2.0.0", "1.9.9", true},
		{"v1.2", "1.2.0", false},
		{"v0.9.0", "1.2.0", false},
	}
	for _, c := range cases {
		if got := versionNewer(c.remote, c.local); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.remote, c.local, got, c.want)
		}
	}
}
