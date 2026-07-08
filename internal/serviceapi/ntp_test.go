package serviceapi

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestBuildSNTPReply_HeaderShape(t *testing.T) {
	req := make([]byte, sntpPacketLen)
	// client version 4, mode 3 (client).
	req[0] = (0 << 6) | (4 << 3) | 3
	// client's transmit timestamp — arbitrary marker so we can verify echo.
	binary.BigEndian.PutUint32(req[40:44], 0x11223344)
	binary.BigEndian.PutUint32(req[44:48], 0x55667788)

	recv := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	tx := recv.Add(1 * time.Millisecond)
	out := buildSNTPReply(req, recv, tx)

	if len(out) != sntpPacketLen {
		t.Fatalf("reply length: want %d got %d", sntpPacketLen, len(out))
	}
	// Mode 4 (server) and version 4 (echoed).
	if got := (out[0] >> 3) & 0x07; got != 4 {
		t.Errorf("VN: want 4 got %d", got)
	}
	if got := out[0] & 0x07; got != 4 {
		t.Errorf("Mode: want 4 (server) got %d", got)
	}
	if out[1] != 1 {
		t.Errorf("Stratum: want 1 got %d", out[1])
	}
	// Reference ID "MACH".
	if string(out[12:16]) != "MACH" {
		t.Errorf("RefID: want MACH got %q", out[12:16])
	}
	// Origin Timestamp = echoed client Transmit Timestamp.
	if binary.BigEndian.Uint32(out[24:28]) != 0x11223344 {
		t.Errorf("Origin secs: want 0x11223344 got %x", out[24:28])
	}
	if binary.BigEndian.Uint32(out[28:32]) != 0x55667788 {
		t.Errorf("Origin frac: want 0x55667788 got %x", out[28:32])
	}
}

func TestBuildSNTPReply_TimestampsMatchTime(t *testing.T) {
	req := make([]byte, sntpPacketLen)
	req[0] = (0 << 6) | (4 << 3) | 3

	// Pick a moment with a known non-zero nanosecond so both halves get
	// exercised.
	recv := time.Unix(1_735_000_000, 500_000_000).UTC()
	tx := recv.Add(1 * time.Millisecond)
	out := buildSNTPReply(req, recv, tx)

	// Receive timestamp round-trip. NTP epoch = UNIX + 2208988800.
	wantSecs := uint32(recv.Unix() + ntpUnixEpochOffset)
	if got := binary.BigEndian.Uint32(out[32:36]); got != wantSecs {
		t.Errorf("Receive secs: want %d got %d", wantSecs, got)
	}
	// Fraction: 500_000_000 ns = 0.5s → 0x80000000.
	if got := binary.BigEndian.Uint32(out[36:40]); got != 0x80000000 {
		t.Errorf("Receive frac: want 0x80000000 (half-second) got 0x%x", got)
	}
}

func TestBuildSNTPReply_EchoesClientVersion3(t *testing.T) {
	req := make([]byte, sntpPacketLen)
	req[0] = (0 << 6) | (3 << 3) | 3 // v3 client
	out := buildSNTPReply(req, time.Now(), time.Now())
	if got := (out[0] >> 3) & 0x07; got != 3 {
		t.Errorf("VN should echo v3 client's version, got %d", got)
	}
}

func TestNTPServer_LivePortEndToEnd(t *testing.T) {
	srv, err := NewNTPServer()
	if err != nil {
		t.Fatalf("NewNTPServer: %v", err)
	}
	if srv.Port() == 0 {
		t.Fatal("Port() should be non-zero after bind")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	// Client side: send an SNTPv4 request, read the reply.
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: srv.Port(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := make([]byte, sntpPacketLen)
	req[0] = (0 << 6) | (4 << 3) | 3 // v4, client
	binary.BigEndian.PutUint32(req[40:44], 0xDEADBEEF)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != sntpPacketLen {
		t.Fatalf("reply length: want %d got %d", sntpPacketLen, n)
	}
	// Origin echo.
	if binary.BigEndian.Uint32(buf[24:28]) != 0xDEADBEEF {
		t.Errorf("Origin should echo client Transmit; got %x", buf[24:28])
	}
	// Transmit Timestamp should decode to a time within ~5s of now.
	txSecs := binary.BigEndian.Uint32(buf[40:44])
	txUnix := int64(txSecs) - ntpUnixEpochOffset
	drift := time.Since(time.Unix(txUnix, 0))
	if drift < -5*time.Second || drift > 5*time.Second {
		t.Errorf("Transmit timestamp drift: %v (want <5s of now)", drift)
	}
}
