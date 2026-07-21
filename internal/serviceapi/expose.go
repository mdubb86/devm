package serviceapi

import (
	"fmt"
	"net"
	"sort"
	"strconv"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
)

// computeExposeMap turns a project config into softnet's declarative
// ingress map: one host listener per DIRECT service port on the
// project's IP, plus SSH on :22. Non-direct services are not exposed
// via softnet — they're reachable (if HTTP) via the daemon proxy on
// :80/:443 instead, or not reachable at all. Every entry binds on
// projectIP (the allocated per-project loopback address from
// AllocateProjectIP). Sorted by guest port for wire-stability.
func computeExposeMap(cfg schema.Config, projectIP string) []softnet.ExposePort {
	var ports []softnet.ExposePort
	for _, svc := range cfg.Services {
		if svc.Port == 0 || !svc.Direct {
			continue
		}
		ports = append(ports, softnet.ExposePort{
			GuestPort: svc.Port,
			BindIP:    projectIP,
			HostPort:  svc.Port,
		})
	}
	// SSH always exposed on :22 for every project.
	ports = append(ports, softnet.ExposePort{
		GuestPort: 22,
		BindIP:    projectIP,
		HostPort:  22,
	})
	sort.Slice(ports, func(i, j int) bool { return ports[i].GuestPort < ports[j].GuestPort })
	return ports
}

// pushExposeMap sends the full expose map to a project's softnet control
// socket. It first reconciles the project's host-port claims against
// every other running project's — a conflict (another project already
// owns one of these bindIP:hostPort endpoints) is returned without
// dispatching to softnet, so the colliding listener is never bound and
// the misrouting `.test` DNS answer (which resolves every name to
// loopback) never happens. The claim reconcile runs even when the
// project has no softnet socket yet, so claims track intent before the
// socket exists. Ingress is independent of egress policy, so this is
// safe to call in any egress state.
//
// Fail-loud contract: every caller of this function is expected to run
// on a path where /vm/start, /vm/apply-iron-proxy (adopt-in-place), or
// discoverSoftnet (daemon-restart rehydration) has already registered
// this project's softnet control socket. An empty softnetState here
// means one of those registration steps was skipped — historically a
// silent no-op ("VM not started, ingress will be re-pushed at the next
// /vm/start"), which hid the exact adopt-in-place gap this comment now
// warns about: pushExposeMap returning nil made it look like ingress
// had been reconciled when nothing was ever dispatched. Return an
// error instead so a missing registration surfaces immediately rather
// than as a silently-unreachable service.
func pushExposeMap(projectID string, ports []softnet.ExposePort) error {
	keys := make([]string, 0, len(ports))
	for _, p := range ports {
		keys = append(keys, net.JoinHostPort(p.BindIP, strconv.Itoa(p.HostPort)))
	}
	if err := exposeClaims.reconcile(projectID, keys); err != nil {
		return err
	}
	sock := softnetState.get(projectID)
	if sock == "" {
		return fmt.Errorf("push expose map for %s: softnet control socket not registered — /vm/start (or /vm/apply-iron-proxy for adopt-in-place) must be called first", projectID)
	}
	if err := newSoftnetClient(sock).setExposeMap(ports); err != nil {
		return fmt.Errorf("push expose map for %s: %w", projectID, err)
	}
	return nil
}
