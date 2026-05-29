package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
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

// applyPublish parses "HOST:SBX" and appends a portMapping to listJSON.
func (s *scriptedRunner) applyPublish(spec string) {
	var host, sbx int
	if n, _ := fmt.Sscanf(spec, "%d:%d", &host, &sbx); n != 2 {
		return
	}
	var current []portMapping
	if s.listJSON != "" && s.listJSON != "[]" {
		_ = json.Unmarshal([]byte(s.listJSON), &current)
	}
	current = append(current, portMapping{
		HostIP: "127.0.0.1", HostPort: host, SandboxPort: sbx, Protocol: "tcp",
	})
	out, _ := json.Marshal(current)
	s.listJSON = string(out)
}

// applyUnpublish parses "HOST:SBX" and removes the matching entry.
func (s *scriptedRunner) applyUnpublish(spec string) {
	var host, sbx int
	if n, _ := fmt.Sscanf(spec, "%d:%d", &host, &sbx); n != 2 {
		return
	}
	var current []portMapping
	if s.listJSON != "" && s.listJSON != "[]" {
		_ = json.Unmarshal([]byte(s.listJSON), &current)
	}
	out := make([]portMapping, 0, len(current))
	for _, m := range current {
		if m.HostPort == host && m.SandboxPort == sbx {
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

func cfgWith(services map[string]schema.Service, portOffset int) schema.Config {
	return schema.Config{
		Project: schema.Project{
			ID:           "x",
			SandboxName:  "x-sbx",
			HostnameApex: "x.local",
			PortOffset:   portOffset,
		},
		Services: services,
	}
}

func TestReconcilePortsAddsMissing(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Canonical: 8080},
	}, 60000)
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
	assert.Contains(t, strings.Join(publishes[0], " "), "68080:8080")
}

func TestReconcilePortsRemovesExtra(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{}, 60000) // no services
	runner := &scriptedRunner{
		listJSON: `[{"host_ip":"127.0.0.1","host_port":68080,"sandbox_port":8080,"protocol":"tcp"}]`,
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
	assert.Contains(t, strings.Join(unpublishes[0], " "), "68080:8080")
}

func TestReconcilePortsNoOpWhenMatching(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Canonical: 8080},
	}, 60000)
	runner := &scriptedRunner{
		listJSON: `[{"host_ip":"127.0.0.1","host_port":68080,"sandbox_port":8080,"protocol":"tcp"}]`,
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
		"api": {Canonical: 8080},
		"db":  {Canonical: 5432},
		"web": {Canonical: 3000},
	}, 60000)
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
	assert.Contains(t, seen[0], "63000:3000")
	assert.Contains(t, seen[1], "65432:5432")
	assert.Contains(t, seen[2], "68080:8080")
}

func TestReconcilePortsBubblesListError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{}, 60000)
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
		"api": {Canonical: 8080},
		"web": {Canonical: 3000},
	}, 60000)
	runner := &scriptedRunner{
		listJSON: `[` +
			`{"host_ip":"127.0.0.1","host_port":68080,"sandbox_port":8080,"protocol":"tcp"},` +
			`{"host_ip":"127.0.0.1","host_port":65432,"sandbox_port":5432,"protocol":"tcp"}` +
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
	assert.Contains(t, publishes[0], "63000:3000")

	require.Len(t, unpublishes, 1, "expected exactly one unpublish for the removed service")
	assert.Contains(t, unpublishes[0], "65432:5432")
}

func TestReconcilePortsBubblesPublishError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{
		"api": {Canonical: 8080},
	}, 60000)
	runner := &scriptedRunner{
		listJSON: "[]",
		failOn:   map[string]error{"--publish": errors.New("publish boom")},
	}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish boom")
	assert.Contains(t, err.Error(), "publish 68080:8080", "error should mention the failing spec")
}

func TestReconcilePortsBubblesParseError(t *testing.T) {
	cfg := cfgWith(map[string]schema.Service{}, 60000)
	runner := &scriptedRunner{listJSON: "not-json"}
	sb := &sandbox.Sandbox{Name: "x-sbx"}
	err := ReconcilePortsWithRunner(sb, cfg, runner)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse ports JSON")
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
