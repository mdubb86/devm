package main

import "testing"

// stubResolveProjectFn swaps discoverProjectFn for the duration of t
// so tests can drive gated verbs with a canned LocalProject instead of
// mounting a real daemon connection.
func stubResolveProjectFn(t *testing.T, name, macCwd string) {
	t.Helper()
	orig := discoverProjectFn
	discoverProjectFn = func() (LocalProject, error) {
		return LocalProject{Name: name, MacCwd: macCwd}, nil
	}
	t.Cleanup(func() { discoverProjectFn = orig })
}
