package mutagen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// LinuxArm64Agent extracts the linux/arm64 mutagen-agent binary from the
// embedded mutagen-agents.tar.gz. Returns the binary's bytes.
//
// The tarball layout: one entry per platform, keyed by GOOS_GOARCH
// (linux_arm64, darwin_arm64, linux_amd64, etc.). Each entry IS the
// agent binary directly — no subdirectory, no inner filename.
//
// devm's guest image is aarch64-only, so we only ever need linux_arm64;
// other-platform bytes stay in the tarball and are ignored.
func LinuxArm64Agent() ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(mutagenAgentsTarGz))
	if err != nil {
		return nil, fmt.Errorf("mutagen agents.tar.gz: gzip open: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("mutagen agents.tar.gz: no linux_arm64 entry")
		}
		if err != nil {
			return nil, fmt.Errorf("mutagen agents.tar.gz: tar read: %w", err)
		}
		if hdr.Name == "linux_arm64" {
			body, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("mutagen agents.tar.gz: read linux_arm64: %w", err)
			}
			return body, nil
		}
	}
}
