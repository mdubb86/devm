package serviceapi

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// listenersBoundTo returns the TCP ports with LISTEN sockets bound
// SPECIFICALLY to ip — never wildcard listeners (`*.<port>`) that
// merely accept on it. A host sshd on `*:22` accepts on every lo0
// alias; a specific-address bind is the signature of a softnet
// (orphan VM) actually holding the IP. Package-level var so unit
// tests can stub the OS query.
var listenersBoundTo = darwinListenersBoundTo

// darwinListenersBoundTo shells out to `netstat -an -f inet` and
// returns the ports whose IPv4 LISTEN rows have a local address
// equal to ip.
func darwinListenersBoundTo(ip string) ([]int, error) {
	out, err := exec.Command("netstat", "-an", "-f", "inet").Output()
	if err != nil {
		return nil, fmt.Errorf("netstat -an -f inet: %w", err)
	}
	return parseNetstatListeners(out, ip), nil
}

// parseNetstatListeners parses `netstat -an -f inet` output and
// returns the ports whose IPv4 LISTEN rows have a local address
// equal to ip. Wildcard rows (`*.<port>`) are skipped. The parser is
// permissive about column spacing but strict about the row shape:
// tcp4 proto, LISTEN state, local address of the form `<ip>.<port>`.
func parseNetstatListeners(output []byte, ip string) []int {
	var ports []int
	s := bufio.NewScanner(bytes.NewReader(output))
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 6 {
			continue
		}
		if fields[0] != "tcp4" {
			continue
		}
		if fields[5] != "LISTEN" {
			continue
		}
		rowIP, port, ok := splitLocalAddr(fields[3])
		if !ok {
			continue
		}
		if rowIP != ip {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}

// splitLocalAddr parses a netstat local-address field of the form
// `<ip>.<port>` into its components. `<ip>` is the string before the
// LAST dot (so dotted IPv4 is preserved); `<port>` is parsed as an
// int. Returns ok=false on malformed input or a non-numeric port.
func splitLocalAddr(addr string) (ip string, port int, ok bool) {
	i := strings.LastIndex(addr, ".")
	if i <= 0 || i == len(addr)-1 {
		return "", 0, false
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return "", 0, false
	}
	return addr[:i], p, true
}
