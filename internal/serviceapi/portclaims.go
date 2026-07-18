package serviceapi

import (
	"fmt"
	"sync"
)

// portClaims tracks which project owns each host bind endpoint
// ("bindIP:hostPort") so the daemon can refuse to point two projects'
// ingress at the same host port — which, with the unified .test->loopback
// DNS, would otherwise silently misroute one project's hostname to the
// other's service.
type portClaims struct {
	mu    sync.Mutex
	owner map[string]string // "bindIP:hostPort" -> projectID
}

func newPortClaims() *portClaims {
	return &portClaims{owner: map[string]string{}}
}

// reconcile atomically makes projectID the sole owner of exactly keys.
// If any key is currently owned by a different project, it returns a
// conflict error and changes nothing (the caller must not proceed with
// the push). Otherwise it releases projectID's stale keys and claims the
// given set.
func (c *portClaims) reconcile(projectID string, keys []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		if o, ok := c.owner[k]; ok && o != projectID {
			return fmt.Errorf("host port %s already in use by project %q (pick a distinct port for %q)", k, o, projectID)
		}
	}
	for k, o := range c.owner {
		if o == projectID {
			delete(c.owner, k)
		}
	}
	for _, k := range keys {
		c.owner[k] = projectID
	}
	return nil
}

func (c *portClaims) release(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, o := range c.owner {
		if o == projectID {
			delete(c.owner, k)
		}
	}
}

var exposeClaims = newPortClaims()
