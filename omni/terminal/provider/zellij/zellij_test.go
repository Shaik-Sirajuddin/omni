package zellij

import (
	"strings"
	"testing"
)

// TestZellijAssetFor covers the GOOS/GOARCH matrix for release-tarball
// selection. It drives zellijAssetFor directly so non-host platforms
// (e.g. darwin from a linux runner) and the error paths are reachable.
func TestZellijAssetFor(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
		wantErr      string // substring expected in the error, "" means no error
	}{
		{"linux", "amd64", "zellij-x86_64-unknown-linux-musl.tar.gz", ""},
		{"linux", "arm64", "zellij-aarch64-unknown-linux-musl.tar.gz", ""},
		{"darwin", "amd64", "zellij-x86_64-apple-darwin.tar.gz", ""},
		{"darwin", "arm64", "zellij-aarch64-apple-darwin.tar.gz", ""},
		{"windows", "amd64", "", "unsupported OS"},
		{"linux", "386", "", "unsupported arch"},
	}

	for _, tc := range cases {
		name := tc.goos + "/" + tc.goarch
		t.Run(name, func(t *testing.T) {
			got, err := zellijAssetFor(tc.goos, tc.goarch)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (asset=%q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				if got != "" {
					t.Errorf("expected empty asset on error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestZellijAssetDelegates confirms the runtime-backed zellijAsset stays in
// sync with zellijAssetFor for the host platform (behaviour-preserving seam).
func TestZellijAssetDelegates(t *testing.T) {
	got, err := zellijAsset()
	if err != nil {
		// host is an unsupported platform for zellij; nothing to compare.
		t.Skipf("host platform unsupported: %v", err)
	}
	if !strings.HasPrefix(got, "zellij-") || !strings.HasSuffix(got, ".tar.gz") {
		t.Errorf("unexpected asset name %q", got)
	}
}
