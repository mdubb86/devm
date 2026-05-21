package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Agent is the in-VM devm daemon. It accepts line-based commands over a
// Unix domain socket and stays alive until it receives SHUTDOWN.
type Agent struct {
	socketPath  string
	idleTimeout time.Duration
	scanner     Scanner

	mu           sync.Mutex
	listener     net.Listener
	startTime    time.Time
	lastActivity time.Time
	lastSessions int
	shutdownCh   chan struct{}
}

// NewAgent constructs an agent that will listen on socketPath, periodically
// poll scanner for active sessions, and update its activity timestamp when
// any sessions are seen. The idleTimeout field is recorded for future use
// but does NOT trigger shutdown in the current implementation — see
// startActivityWatcher.
func NewAgent(socketPath string, idleTimeout time.Duration, scanner Scanner) *Agent {
	now := time.Now()
	return &Agent{
		socketPath:   socketPath,
		idleTimeout:  idleTimeout,
		scanner:      scanner,
		startTime:    now,
		lastActivity: now,
		shutdownCh:   make(chan struct{}),
	}
}

// Serve listens for connections and handles them. Returns nil after Shutdown.
func (a *Agent) Serve() error {
	if err := os.MkdirAll(filepath.Dir(a.socketPath), 0o755); err != nil {
		return fmt.Errorf("mkdir socket parent: %w", err)
	}
	_ = os.Remove(a.socketPath) // remove stale socket if any
	l, err := net.Listen("unix", a.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	a.mu.Lock()
	a.listener = l
	a.mu.Unlock()

	a.startActivityWatcher()

	// Accept loop runs until listener is closed (by Shutdown).
	for {
		conn, err := l.Accept()
		if err != nil {
			// If we've shut down, this is expected.
			select {
			case <-a.shutdownCh:
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go a.handleConn(conn)
	}
}

// Shutdown closes the listener, removes the socket, signals any background
// goroutines. Safe to call multiple times.
func (a *Agent) Shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.shutdownCh:
		return // already shut down
	default:
		close(a.shutdownCh)
	}
	if a.listener != nil {
		_ = a.listener.Close()
	}
	_ = os.Remove(a.socketPath)
}

func (a *Agent) handleConn(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)
	switch cmd {
	case "PING":
		fmt.Fprintln(conn, "PONG")
	case "STATUS":
		a.mu.Lock()
		uptime := int(time.Since(a.startTime).Seconds())
		idle := int(time.Since(a.lastActivity).Seconds())
		sessions := a.lastSessions
		a.mu.Unlock()
		fmt.Fprintf(conn, "OK uptime=%d idle=%d sessions=%d\n", uptime, idle, sessions)
	case "SHUTDOWN":
		fmt.Fprintln(conn, "BYE")
		// Schedule shutdown so the response actually flushes.
		go func() {
			time.Sleep(50 * time.Millisecond)
			a.Shutdown()
		}()
	default:
		fmt.Fprintln(conn, "ERR unknown command")
	}
}

// startActivityWatcher launches a background goroutine that polls the
// scanner. When any sessions are seen the activity timestamp is bumped.
//
// IMPORTANT: this watcher does NOT call Shutdown. The intent of this
// implementation phase is to observe what the scanner reports without
// risking a sandbox killing itself during development. Once we can drive
// real interactive sessions through the agent we will add the staleness
// check + Shutdown call here.
func (a *Agent) startActivityWatcher() {
	if a.scanner == nil {
		return
	}
	tick := a.idleTimeout / 10
	if tick < time.Second {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-a.shutdownCh:
				return
			case <-ticker.C:
				n := a.scanner.Sessions()
				a.mu.Lock()
				a.lastSessions = n
				if n > 0 {
					a.lastActivity = time.Now()
				}
				a.mu.Unlock()
			}
		}
	}()
}

// idleTimeoutFromEnv parses DEVM_AGENT_IDLE_TIMEOUT (e.g. "30m", "5s")
// and returns the default 30m if unset or invalid.
func idleTimeoutFromEnv() time.Duration {
	if raw := os.Getenv("DEVM_AGENT_IDLE_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Minute
}

func main() {
	socket := "/tmp/devm-agent.sock"
	if s := os.Getenv("DEVM_AGENT_SOCKET"); s != "" {
		socket = s
	}
	procRoot := "/proc"
	if p := os.Getenv("DEVM_AGENT_PROC"); p != "" {
		procRoot = p
	}
	a := NewAgent(socket, idleTimeoutFromEnv(), NewProcScanner(procRoot))
	if err := a.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
