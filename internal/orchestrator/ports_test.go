package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedRunner returns canned bytes for specific command shapes and
// records all invocations.
type scriptedRunner struct {
	calls    [][]string
	listJSON string
	failOn   map[string]error // keyed by joined-arg substring
}

func (s *scriptedRunner) Output(name string, args ...string) ([]byte, error) {
	all := append([]string{name}, args...)
	s.calls = append(s.calls, all)
	joined := strings.Join(all, " ")
	for sub, e := range s.failOn {
		if strings.Contains(joined, sub) {
			return nil, e
		}
	}
	if strings.Contains(joined, "ports") && strings.Contains(joined, "--json") {
		return []byte(s.listJSON), nil
	}
	// Stateful stub: when the orchestrator publishes/unpublishes a
	// port, update listJSON so verify-after-publish polling sees the
	// new state. Mirrors how the real sbx daemon would behave.
	if i := indexOf(args, "--publish"); i >= 0 && i+1 < len(args) {
		s.applyPublish(args[i+1])
	}
	if i := indexOf(args, "--unpublish"); i >= 0 && i+1 < len(args) {
		s.applyUnpublish(args[i+1])
	}
	return []byte(""), nil
}

func indexOf(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

// parsePublishSpec splits "[IP:]HOST:SBX" into (ip, host, sbx).
// IP defaults to "127.0.0.1" when absent. Matches the wire forms devm
// emits: bare "H:S" or prefixed "IP:H:S".
func parsePublishSpec(spec string) (ip string, host, sbx int, ok bool) {
	ip = "127.0.0.1"
	if n, _ := fmt.Sscanf(spec, "%d:%d", &host, &sbx); n == 2 {
		return ip, host, sbx, true
	}
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) != 3 {
		return "", 0, 0, false
	}
	if n, _ := fmt.Sscanf(parts[1]+":"+parts[2], "%d:%d", &host, &sbx); n != 2 {
		return "", 0, 0, false
	}
	return parts[0], host, sbx, true
}

// applyPublish parses "[IP:]HOST:SBX" and appends a portMapping to listJSON.
func (s *scriptedRunner) applyPublish(spec string) {
	ip, host, sbx, ok := parsePublishSpec(spec)
	if !ok {
		return
	}
	var current []portMapping
	if s.listJSON != "" && s.listJSON != "[]" {
		_ = json.Unmarshal([]byte(s.listJSON), &current)
	}
	current = append(current, portMapping{
		HostIP: ip, HostPort: host, SandboxPort: sbx, Protocol: "tcp",
	})
	out, _ := json.Marshal(current)
	s.listJSON = string(out)
}

// applyUnpublish parses "[IP:]HOST:SBX" and removes the matching entry.
func (s *scriptedRunner) applyUnpublish(spec string) {
	ip, host, sbx, ok := parsePublishSpec(spec)
	if !ok {
		return
	}
	var current []portMapping
	if s.listJSON != "" && s.listJSON != "[]" {
		_ = json.Unmarshal([]byte(s.listJSON), &current)
	}
	out := make([]portMapping, 0, len(current))
	for _, m := range current {
		if m.HostPort == host && m.SandboxPort == sbx && m.HostIP == ip {
			continue
		}
		out = append(out, m)
	}
	js, _ := json.Marshal(out)
	s.listJSON = string(js)
}
func (s *scriptedRunner) Run(name string, args ...string) error {
	all := append([]string{name}, args...)
	s.calls = append(s.calls, all)
	joined := strings.Join(all, " ")
	for sub, e := range s.failOn {
		if strings.Contains(joined, sub) {
			return e
		}
	}
	return nil
}
func (s *scriptedRunner) RunStdin(stdin, name string, args ...string) error {
	return s.Run(name, args...)
}

func cfgWith(services map[string]schema.Service) schema.Config {
	return schema.Config{
		Project: schema.Project{
			ID:          "x",
			SandboxName: "x-sbx",
		},
		Services: services,
	}
}

func TestReconcilePortsAddsMissing(t *testing.T) {
	// Tart VMs: hostPort == sandboxPort (no offset).
	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
	})
	runner := &scriptedRunner{listJSON: "[]"} // nothing currently published

	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.NoError(t, err)

	// Expect exactly one --publish call with the right mapping.
	var publishes [][]string
	for _, c := range runner.calls {
		if hasFlag(c, "--publish") {
			publishes = append(publishes, c)
		}
	}
	require.Len(t, publishes, 1)
	assert.Contains(t, strings.Join(publishes[0], " "), "8080:8080")
}

func TestReconcilePortsRemovesExtra(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{}) // no services
	runner := &scriptedRunner{
		listJSON: `[{"host_ip":"127.0.0.1","host_port":8080,"sandbox_port":8080,"protocol":"tcp"}]`,
	}

	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.NoError(t, err)

	var unpublishes [][]string
	for _, c := range runner.calls {
		if hasFlag(c, "--unpublish") {
			unpublishes = append(unpublishes, c)
		}
	}
	require.Len(t, unpublishes, 1)
	assert.Contains(t, strings.Join(unpublishes[0], " "), "8080:8080")
}

func TestReconcilePortsNoOpWhenMatching(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
	})
	runner := &scriptedRunner{
		listJSON: `[{"host_ip":"127.0.0.1","host_port":8080,"sandbox_port":8080,"protocol":"tcp"}]`,
	}

	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.NoError(t, err)

	for _, c := range runner.calls {
		require.False(t, hasFlag(c, "--publish"), "should not publish when already matching: %v", c)
		require.False(t, hasFlag(c, "--unpublish"), "should not unpublish when matching: %v", c)
	}
}

func TestReconcilePortsHandlesMultipleServicesDeterministically(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
		"db":  {Port: 5432},
		"web": {Port: 3000},
	})
	runner := &scriptedRunner{listJSON: "[]"}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	require.NoError(t, ReconcilePortsWithRunner(sb, cfg, runner))

	var seen []string
	for _, c := range runner.calls {
		if hasFlag(c, "--publish") {
			seen = append(seen, strings.Join(c, " "))
		}
	}
	require.Len(t, seen, 3)
	// Order must be deterministic — sort the slice and verify contents.
	sort.Strings(seen)
	assert.Contains(t, seen[0], "3000:3000")
	assert.Contains(t, seen[1], "5432:5432")
	assert.Contains(t, seen[2], "8080:8080")
}

func TestReconcilePortsBubblesListError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{})
	runner := &scriptedRunner{
		failOn: map[string]error{"--json": errors.New("boom")},
	}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.Contains(t, err.Error(), "list ports", "error should originate from the list step, not publish/unpublish")
}

func TestReconcilePortsMixedAddRemoveKeep(t *testing.T) {
	// Desired: api(8080), web(3000). Current: api(8080) — keep,
	// db(5432) — remove. Expect 1 publish (web) and 1 unpublish (db),
	// nothing for api.
	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
		"web": {Port: 3000},
	})
	runner := &scriptedRunner{
		listJSON: `[` +
			`{"host_ip":"127.0.0.1","host_port":8080,"sandbox_port":8080,"protocol":"tcp"},` +
			`{"host_ip":"127.0.0.1","host_port":5432,"sandbox_port":5432,"protocol":"tcp"}` +
			`]`,
	}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	require.NoError(t, ReconcilePortsWithRunner(sb, cfg, runner))

	var publishes, unpublishes []string
	for _, c := range runner.calls {
		joined := strings.Join(c, " ")
		switch {
		case hasFlag(c, "--publish"):
			publishes = append(publishes, joined)
		case hasFlag(c, "--unpublish"):
			unpublishes = append(unpublishes, joined)
		}
	}
	require.Len(t, publishes, 1, "expected exactly one publish for the new service")
	assert.Contains(t, publishes[0], "3000:3000")

	require.Len(t, unpublishes, 1, "expected exactly one unpublish for the removed service")
	assert.Contains(t, unpublishes[0], "5432:5432")
}

func TestReconcilePortsBubblesPublishError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
	})
	runner := &scriptedRunner{
		listJSON: "[]",
		failOn:   map[string]error{"--publish": errors.New("publish boom")},
	}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish boom")
	assert.Contains(t, err.Error(), "publish 127.0.0.1:8080:8080", "error should mention the failing spec")
}

func TestReconcilePortsBubblesParseError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{})
	runner := &scriptedRunner{listJSON: "not-json"}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse ports JSON")
}

// flakeyRunner wraps scriptedRunner: returns publishErr for the first
// failTimes --publish calls, then delegates to the inner runner (which
// will succeed and update its listJSON). Lets us simulate sbx's
// "endpoint not ready yet" transient.
type flakeyRunner struct {
	inner        *scriptedRunner
	publishErr   error
	failTimes    int
	publishCalls int
}

func (f *flakeyRunner) Output(name string, args ...string) ([]byte, error) {
	if indexOf(args, "--publish") >= 0 && f.publishCalls < f.failTimes {
		f.publishCalls++
		// Still record the call on the inner runner so test assertions
		// can introspect them.
		f.inner.calls = append(f.inner.calls, append([]string{name}, args...))
		return nil, f.publishErr
	}
	return f.inner.Output(name, args...)
}
func (f *flakeyRunner) Run(name string, args ...string) error {
	return f.inner.Run(name, args...)
}
func (f *flakeyRunner) RunStdin(stdin, name string, args ...string) error {
	return f.inner.RunStdin(stdin, name, args...)
}

func TestPublishWithVerifyRetriesOnEndpointNotReady(t *testing.T) {
	// sbx returns this error when the sandbox is "running" but its
	// container endpoint hasn't been assigned an IP yet. Under the
	// new anchor-alive flow, port reconcile runs immediately after
	// exec-ready, which is a race with endpoint allocation.
	endpointErr := errors.New(
		"sbx ports --publish 8080:8080: exit status 1: " +
			"ERROR: publish port: failed to resolve endpoint: " +
			"no container endpoint with IP address found")

	cfg := cfgWith(map[string]schema.Service{
		"api": {Port: 8080},
	})
	runner := &flakeyRunner{
		inner:      &scriptedRunner{listJSON: "[]"},
		publishErr: endpointErr,
		failTimes:  2, // fail twice, succeed on third attempt
	}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.NoError(t, err, "publishWithVerify should retry through transient endpoint-not-ready")
	assert.Equal(t, 2, runner.publishCalls,
		"should have hit the transient error exactly failTimes times before succeeding")
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
