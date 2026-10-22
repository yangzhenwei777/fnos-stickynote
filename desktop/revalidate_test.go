package main

import (
	"testing"
	"time"
)

// 30 天重验门控纯函数（N=30 已拍板；到期语义=需联网换票重验）。
func TestShouldRevalidate(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last time.Time
		want bool
	}{
		{"never-revalidated", time.Time{}, true},
		{"29d-ago", now.Add(-29 * 24 * time.Hour), false},
		{"exactly-30d", now.Add(-30 * 24 * time.Hour), true},
		{"31d-ago", now.Add(-31 * 24 * time.Hour), true},
		{"fresh", now.Add(-time.Minute), false},
		{"future-clock-skew", now.Add(time.Hour), false}, // 时钟回拨不误判到期
	}
	for _, c := range cases {
		if got := shouldRevalidate(c.last, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
