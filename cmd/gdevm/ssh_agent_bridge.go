package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// sshAgentBridgeMain runs the guest-side half of the ssh-agent bridge:
// a unix-socket listener on --sock, forwarding each accepted stream
// to --mac-host:--mac-port. No filtering here — the Mac-side bridge
// is where the filter lives. Returns 0 on clean shutdown (signal /
// socket closed), non-zero on fatal bind error.
func sshAgentBridgeMain(args []string) int {
	fs := flag.NewFlagSet("ssh-agent-bridge", flag.ContinueOnError)
	var sockPath, macHost string
	var macPort int
	fs.StringVar(&sockPath, "sock", "/run/devm/ssh-agent.sock", "unix socket path")
	fs.StringVar(&macHost, "mac-host", "", "Mac-side NAT alias IP")
	fs.IntVar(&macPort, "mac-port", 0, "Mac-side bridge TCP port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if macHost == "" || macPort == 0 {
		fmt.Fprintln(os.Stderr, "ssh-agent-bridge: --mac-host and --mac-port are required")
		return 2
	}

	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-agent-bridge: mkdir: %v\n", err)
		return 1
	}
	_ = os.Remove(sockPath)
	oldUmask := syscall.Umask(0o177) // ensure 0600 on created socket
	ln, err := net.Listen("unix", sockPath)
	syscall.Umask(oldUmask)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-agent-bridge: listen %s: %v\n", sockPath, err)
		return 1
	}
	defer ln.Close()

	for {
		c, err := ln.Accept()
		if err != nil {
			return 0
		}
		go spliceToMac(c, macHost, macPort)
	}
}

func spliceToMac(guest net.Conn, macHost string, macPort int) {
	defer guest.Close()
	mac, err := net.Dial("tcp", net.JoinHostPort(macHost, strconv.Itoa(macPort)))
	if err != nil {
		return
	}
	defer mac.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(mac, guest)
		if t, ok := mac.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guest, mac)
		if u, ok := guest.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
	}()
	wg.Wait()
}
