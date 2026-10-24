package transfers_test

import (
	"testing"

	"github.com/shashankpatil-dev/sftpgate/internal/transfers"
)

func TestEnabledHere(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		hostname string
		want     bool
	}{
		{"on by default", nil, "sftpgate-0", true},
		{"switched off", map[string]string{"SFTPGATE_TRANSFERS": "off"}, "sftpgate-0", false},
		{"only on this pod", map[string]string{"SFTPGATE_TRANSFERS_ONLY_ON": "sftpgate-0"}, "sftpgate-0", true},
		{"only on another pod", map[string]string{"SFTPGATE_TRANSFERS_ONLY_ON": "sftpgate-0"}, "sftpgate-1", false},
		{"off wins", map[string]string{"SFTPGATE_TRANSFERS": "off", "SFTPGATE_TRANSFERS_ONLY_ON": "sftpgate-0"}, "sftpgate-0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			if got := transfers.EnabledHere(getenv, tc.hostname); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
