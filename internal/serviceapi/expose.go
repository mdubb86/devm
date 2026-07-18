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
// ingress map: one host listener per service port, plus SSH when a host
// port has been allocated. Every service with a nonzero Port gets a
// listener bound on its ResolveBind() address forwarding host:Port ->
// guest:Port (host port equals guest port; the client connects to the
// declared port and DNS answers host loopback). SSH is bound on loopback
// at the daemon-picked sshHostPort forwarding to guest:22. A zero
// sshHostPort means SSH is not yet allocated and is omitted.
//
// The result is sorted by guest port for a stable wire payload (the
// reconcile in ingress.apply keys on host port, so ordering does not
// affect correctness, only diff noise in logs and tests).
func computeExposeMap(cfg schema.Config, sshHostPort int) []softnet.ExposePort {
	var ports []softnet.ExposePort
	for _, svc := range cfg.Services {
		if svc.Port == 0 {
			continue
		}
		ports = append(ports, softnet.ExposePort{
			GuestPort: svc.Port,
			BindIP:    svc.ResolveBind(),
			HostPort:  svc.Port,
		})
	}
	if sshHostPort != 0 {
		ports = append(ports, softnet.ExposePort{
			GuestPort: 22,
			BindIP:    softnet.HostLoopIP,
			HostPort:  sshHostPort,
		})
	}
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
// project has no softnet socket yet (VM not started), so claims track
// intent before the socket exists; pushExposeMap is then a no-op until
// the socket is registered — ingress is re-pushed at the next
// /vm/start. Ingress is independent of egress policy, so this is safe
// to call in any egress state.
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
		return nil
	}
	if err := newSoftnetClient(sock).setExposeMap(ports); err != nil {
		return fmt.Errorf("push expose map for %s: %w", projectID, err)
	}
	return nil
}
