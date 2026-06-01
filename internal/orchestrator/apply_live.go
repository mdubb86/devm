package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mtwaage/devm/internal/render"
	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
)

// ApplyLive runs every BucketLive change through the corresponding sbx
// command. Non-LIVE changes in the slice are skipped silently (caller
// is expected to handle them via the recreate path). portOffset is the
// project's port_offset, used to compute the host port for each
// canonical port (host = offset + canonical).
//
// Template changes are coalesced — any number of KindTemplateChange
// entries trigger a SINGLE invocation of the in-sandbox dispatcher,
// which re-runs every installer (cheap; identical content is an
// idempotent atomic rewrite). The installer scripts are written to
// .devm/templates/ immediately before the dispatcher runs, so the
// sandbox always executes the latest rendered content. For each
// changed template, this function logs a "consuming services may need
// restart" line to stderr.
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(sb *sandbox.Sandbox, changes []Change, portOffset int, cfg schema.Config, repoRoot string) error {
	var templateChanges []Change
	for _, c := range changes {
		if c.Bucket() != BucketLive {
			continue
		}
		switch c.Kind {
		case KindPortAdd:
			sandboxPort, err := strconv.Atoi(c.Key)
			if err != nil {
				return fmt.Errorf("apply_live: port_add: bad sandbox port %q: %w", c.Key, err)
			}
			spec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+sandboxPort, sandboxPort)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--publish", spec); err != nil {
				return fmt.Errorf("apply_live: sbx ports --publish %s: %w", spec, err)
			}
		case KindPortRemove:
			sandboxPort, err := strconv.Atoi(c.Key)
			if err != nil {
				return fmt.Errorf("apply_live: port_remove: bad sandbox port %q: %w", c.Key, err)
			}
			spec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+sandboxPort, sandboxPort)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--unpublish", spec); err != nil {
				return fmt.Errorf("apply_live: sbx ports --unpublish %s: %w", spec, err)
			}
		case KindPortChange:
			oldP, err := strconv.Atoi(c.Old)
			if err != nil {
				return fmt.Errorf("apply_live: port_change: bad old port %q: %w", c.Old, err)
			}
			newP, err := strconv.Atoi(c.New)
			if err != nil {
				return fmt.Errorf("apply_live: port_change: bad new port %q: %w", c.New, err)
			}
			oldSpec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+oldP, oldP)
			newSpec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+newP, newP)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--unpublish", oldSpec); err != nil {
				return fmt.Errorf("apply_live: port_change: unpublish %s: %w", oldSpec, err)
			}
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--publish", newSpec); err != nil {
				return fmt.Errorf("apply_live: port_change: publish %s: %w", newSpec, err)
			}
		case KindNetworkAdd:
			if err := sb.Runner.Run("sbx", "policy", "allow", "network", c.Key); err != nil {
				return fmt.Errorf("apply_live: sbx policy allow network %s: %w", c.Key, err)
			}
		case KindTemplateChange:
			templateChanges = append(templateChanges, c)
		}
	}

	if len(templateChanges) > 0 {
		// Write updated installer scripts before running the dispatcher so
		// the sandbox executes the latest rendered content. This must happen
		// here (not in the pre-diff WriteDevmDirStaticOnly call in RunReconcile)
		// so the on-disk installers remain as the diff baseline until the
		// change has been detected and we're committed to applying it.
		if err := render.WriteTemplateInstallers(cfg, repoRoot); err != nil {
			return fmt.Errorf("apply_live: write template installers: %w", err)
		}
		// Single dispatcher invocation re-runs all installers. Flags:
		//   -u root: installers write to /etc/ and similar system paths;
		//     root matches the "user: 0" in the spec.yaml startup step.
		//   -e WORKSPACE_DIR=<repoRoot>: non-interactive sbx exec does not
		//     set WORKSPACE_DIR automatically (only available in the sbx
		//     daemon startup context); the dispatcher uses it to glob the
		//     templates dir.
		// Use Output (not Run) so any failure includes the sbx stderr text.
		dispatcherPath := filepath.Join(repoRoot, ".devm", "scripts", "install-templates.sh")
		if _, err := sb.Runner.Output("sbx", "exec",
			"-u", "root",
			"-e", "WORKSPACE_DIR="+repoRoot,
			sb.Name,
			"bash", dispatcherPath); err != nil {
			return fmt.Errorf("apply_live: install-templates: %w", err)
		}
		// User-facing "you might need to restart your service" hint.
		for _, c := range templateChanges {
			// Structural invariants (same as the rest of the Change contract):
			//   add    -> Old == "" && New != ""
			//   change -> Old != "" && New != ""
			//   remove -> Old != "" && New == ""
			if c.New == "" {
				// removed: the on-disk artifact in the sandbox persists.
				fmt.Fprintf(os.Stderr,
					"template %s removed from config; sandbox file persists until recreate.\n",
					c.Detail)
				continue
			}
			action := "updated"
			if c.Old == "" {
				action = "installed"
			}
			fmt.Fprintf(os.Stderr,
				"template %s (service %s) %s; restart consuming services in the shell if needed.\n",
				c.Detail, c.Service, action)
		}
	}
	return nil
}
