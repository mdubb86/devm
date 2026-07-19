// Package main is the devm root port-binder helper.
//
// Runs as a root LaunchDaemon (com.devm.portbinder). On startup it
// provisions lo0 aliases for the devm project-IP pool (127.42.0.1..20),
// then serves bind requests from the user-mode devm daemon over a UDS
// at /var/run/devm-portbinder.sock. Each request is one line of JSON:
//
//	{"op":"bind","ip":"127.42.0.5","port":80,"proto":"tcp"}
//
// The helper binds the requested socket and hands the FD back via
// SCM_RIGHTS. Access control: UDS mode 0660, group _devm; requesting
// process EUID must be a member of _devm.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

const (
	socketPath = "/var/run/devm-portbinder.sock"
	poolStart  = 1
	poolEnd    = 20
	poolFmt    = "127.42.0.%d"
)

type request struct {
	Op    string `json:"op"`
	IP    string `json:"ip"`
	Port  int    `json:"port"`
	Proto string `json:"proto"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func main() {
	log.SetPrefix("devm-portbinder: ")
	log.SetFlags(log.LstdFlags)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := provisionAliases(); err != nil {
		return fmt.Errorf("provision aliases: %w", err)
	}
	return serve()
}

// provisionAliases adds each pool address as an lo0 alias. Idempotent:
// re-adding an existing alias succeeds with no output on macOS.
func provisionAliases() error {
	for n := poolStart; n <= poolEnd; n++ {
		ip := fmt.Sprintf(poolFmt, n)
		cmd := exec.Command("/sbin/ifconfig", "lo0", "alias", ip, "up")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ifconfig lo0 alias %s: %w (%s)", ip, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// serve opens the UDS, handles one request per accepted connection, and
// closes. Runs until process exit.
func serve() error {
	_ = os.Remove(socketPath) // remove any stale socket from a prior run
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	defer ln.Close()

	// Chmod to 0660; chown to root:_devm (root is default; only chgrp needed).
	// _devm group is created at install time; if it doesn't exist, chmod
	// still applies and the UDS is root-only until install completes.
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return fmt.Errorf("chmod %s: %w", socketPath, err)
	}
	if grp, gerr := lookupDevmGroup(); gerr == nil {
		_ = os.Chown(socketPath, 0, grp)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(conn)
	}
}

// handle reads exactly one line of JSON, replies with a JSON status,
// and — on success — passes the newly-bound FD via SCM_RIGHTS.
func handle(conn net.Conn) {
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}

	br := bufio.NewReader(uc)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	req, err := parseRequest(bytes.TrimRight(line, "\n"))
	if err != nil {
		writeErr(uc, err.Error())
		return
	}
	if err := validateIPInPool(req.IP); err != nil {
		writeErr(uc, err.Error())
		return
	}
	if req.Proto != "tcp" {
		writeErr(uc, "unsupported proto: "+req.Proto)
		return
	}

	fd, err := bindTCP(req.IP, req.Port)
	if err != nil {
		writeErr(uc, err.Error())
		return
	}
	defer syscall.Close(fd)

	// Reply payload = JSON status; FD rides SCM_RIGHTS on the same write.
	resp, _ := json.Marshal(response{OK: true})
	oob := syscall.UnixRights(fd)
	if _, _, err := uc.WriteMsgUnix(resp, oob, nil); err != nil {
		log.Printf("write reply: %v", err)
	}
}

func writeErr(uc *net.UnixConn, msg string) {
	resp, _ := json.Marshal(response{OK: false, Error: msg})
	_, _ = uc.Write(resp)
}

// parseRequest decodes a bind request. Rejects unknown ops early.
func parseRequest(raw []byte) (request, error) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, err
	}
	if req.Op == "" {
		return req, errors.New("missing op")
	}
	if req.Op != "bind" {
		return req, fmt.Errorf("unknown op: %s", req.Op)
	}
	return req, nil
}

// validateIPInPool returns nil iff ip is exactly a devm pool address
// (127.42.0.N for N in poolStart..poolEnd).
func validateIPInPool(ip string) error {
	for n := poolStart; n <= poolEnd; n++ {
		if ip == fmt.Sprintf(poolFmt, n) {
			return nil
		}
	}
	return fmt.Errorf("ip not in devm pool: %s", ip)
}

// bindTCP opens a TCP listening socket bound to ip:port and returns the
// FD. The caller is responsible for closing it (we duplicate via
// SCM_RIGHTS, so the client's copy stays open after our syscall.Close).
func bindTCP(ip string, port int) (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("setsockopt SO_REUSEADDR: %w", err)
	}
	addr := &syscall.SockaddrInet4{Port: port}
	copy(addr.Addr[:], net.ParseIP(ip).To4())
	if err := syscall.Bind(fd, addr); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("bind %s:%d: %w", ip, port, err)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("listen %s:%d: %w", ip, port, err)
	}
	return fd, nil
}

// lookupDevmGroup returns the numeric gid for the _devm group.
// Returns an error if the group doesn't exist (install hasn't run yet).
func lookupDevmGroup() (int, error) {
	g, err := user.LookupGroup("_devm")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}
