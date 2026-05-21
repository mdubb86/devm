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
// Unix domain socket and stays alive until it receives SHUTDOWN or its
// idle timer fires.
type Agent struct {
	socketPath  string
	idleTimeout time.Duration

	mu            sync.Mutex
	listener      net.Listener
	startTime     time.Time
	lastHeartbeat time.Time
	shutdownCh    chan struct{}
}

// NewAgent constructs an agent that will listen on socketPath with the
// given idleTimeout. socketPath's parent dir must exist (or be creatable).
func NewAgent(socketPath string, idleTimeout time.Duration) *Agent {
	now := time.Now()
	return &Agent{
		socketPath:    socketPath,
		idleTimeout:   idleTimeout,
		startTime:     now,
		lastHeartbeat: now,
		shutdownCh:    make(chan struct{}),
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

// Shutdown closes the listener, removes the socket, signals the idle
// goroutine (if running). Safe to call multiple times.
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

func (a *Agent) bump() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastHeartbeat = time.Now()
}

func (a *Agent) handleConn(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)
	a.bump() // any command bumps the activity timestamp
	switch cmd {
	case "PING":
		fmt.Fprintln(conn, "PONG")
	case "STATUS":
		a.mu.Lock()
		uptime := int(time.Since(a.startTime).Seconds())
		idle := int(time.Since(a.lastHeartbeat).Seconds())
		a.mu.Unlock()
		fmt.Fprintf(conn, "OK uptime=%d idle=%d\n", uptime, idle)
	case "HEARTBEAT":
		fmt.Fprintln(conn, "OK")
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
	a := NewAgent(socket, idleTimeoutFromEnv())
	if err := a.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
