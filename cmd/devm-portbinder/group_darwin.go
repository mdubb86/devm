package main

import "os/user"

// lookupGroup returns the numeric gid for the named group on macOS.
func lookupGroup(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	var gid int
	if _, err := fmtSscan(g.Gid, &gid); err != nil {
		return 0, err
	}
	return gid, nil
}

func fmtSscan(s string, v *int) (int, error) {
	// Local wrapper for fmt.Sscan to avoid an import in main.go.
	return sscan(s, v)
}
