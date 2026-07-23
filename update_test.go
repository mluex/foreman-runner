package main

import "testing"

func TestParseChecksum(t *testing.T) {
	sums := []byte("aaa111  foreman-runner-linux-amd64\n" +
		"bbb222  foreman-runner-darwin-arm64\n" +
		"ccc333 *foreman-runner-linux-arm64\n")

	cases := map[string]string{
		"foreman-runner-linux-amd64":  "aaa111",
		"foreman-runner-darwin-arm64": "bbb222",
		"foreman-runner-linux-arm64":  "ccc333",
	}
	for asset, want := range cases {
		got, err := parseChecksum(sums, asset)
		if err != nil {
			t.Fatalf("parseChecksum(%q) returned error: %v", asset, err)
		}
		if got != want {
			t.Errorf("parseChecksum(%q) = %q, want %q", asset, got, want)
		}
	}
}

func TestParseChecksumMissing(t *testing.T) {
	sums := []byte("aaa111  foreman-runner-linux-amd64\n")
	if _, err := parseChecksum(sums, "foreman-runner-windows-amd64"); err == nil {
		t.Fatal("expected an error for a missing asset, got nil")
	}
}
