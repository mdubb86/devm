// Package image holds the source-of-truth definition of devm's base
// Tart VM image: the script that provisions the guest, plus the
// systemd units and docs describing what runs inside it. It exists
// so a human can read what the VM does without a Go toolchain, and
// so internal/image can embed provision-base.sh at compile time
// instead of shelling out to it — devm doesn't depend on this
// directory existing on disk at install time; the content ships
// inside the binary.
//
// Go's //go:embed directives cannot reach outside the directory tree
// containing the source file (no ".." path elements), which is why
// this tiny embedding shim lives here rather than in
// internal/image — that package imports the exported var below.
package image

import _ "embed"

// ProvisionBaseScript is the verbatim content of provision-base.sh,
// baked in at compile time. internal/image.BuildBaseImage pipes it
// to `tart exec -i devm-base sudo bash -s` to provision the guest.
//
//go:embed provision-base.sh
var ProvisionBaseScript string
