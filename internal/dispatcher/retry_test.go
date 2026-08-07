package dispatcher_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/dispatcher"
)

func TestRetryAfterSecondsCeil(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 1},
		{100 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{3 * time.Second, 3},
	}
	for _, tc := range cases {
		if got := dispatcher.RetryAfterSeconds(tc.d); got != tc.want {
			t.Fatalf("RetryAfterSeconds(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}
