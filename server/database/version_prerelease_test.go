package database

import "testing"

func TestCompareVersionsWithPrerelease(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "prerelease keeps patch version", a: "2.6.5-R1", b: "2.6.5", want: 0},
		{name: "prerelease greater than previous", a: "2.6.5-R1", b: "2.6.4", want: 1},
		{name: "release greater than previous prerelease", a: "2.6.6", b: "2.6.5-R3", want: 1},
		{name: "prerelease less than next minor", a: "2.6.5-R3", b: "2.6.6", want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareVersions(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
