package worker

import "testing"

func TestClassifyExitKeepsTheEnumClosed(t *testing.T) {
	cases := []struct {
		planned bool
		code    int
		raw     string
		want    string
	}{
		{true, 0, "", "planned"},
		{false, 1, "", "crash"},
		{false, 1, "acquire timeout", "timeout"},
		{false, 1, "protocol mismatch", "protocol"},
		{false, 0, "token=secret", "other"},
	}
	for _, tc := range cases {
		if got := ClassifyExit(tc.planned, tc.code, tc.raw); got != tc.want {
			t.Fatalf("ClassifyExit(%v, %d, %q)=%s want %s", tc.planned, tc.code, tc.raw, got, tc.want)
		}
	}
}
