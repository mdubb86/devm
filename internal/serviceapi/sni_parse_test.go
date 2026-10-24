package serviceapi

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestParseSNI_FromRealClientHello(t *testing.T) {
	// Produce a real ClientHello by starting a tls.Dial against a
	// throwaway listener and reading what the client sends.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hello := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		hello <- buf[:n]
	}()
	go func() {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			ServerName: "api.sibling.test", InsecureSkipVerify: true,
		})
		if err == nil {
			c.Close()
		}
	}()
	select {
	case b := <-hello:
		got, err := parseSNI(b)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got != "api.sibling.test" {
			t.Fatalf("want api.sibling.test, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ClientHello")
	}
}

func TestParseSNI_Malformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"not_tls_record":   {0x14, 0x03, 0x03, 0x00, 0x05, 0, 0, 0, 0, 0},
		"truncated_header": {0x16, 0x03},
		"truncated_body":   {0x16, 0x03, 0x01, 0x00, 0x50, 0x01},
	}
	for name, b := range cases {
		if _, err := parseSNI(b); err == nil {
			t.Fatalf("%s: want error, got nil", name)
		}
	}
}

func TestParseSNI_MissingExtension(t *testing.T) {
	// A valid ClientHello without the SNI extension yields "" and nil.
	b := bytes.Join([][]byte{
		{0x16, 0x03, 0x01, 0x00, 0x3b}, // record: handshake, TLS 1.0, len=59
		{0x01, 0x00, 0x00, 0x37},       // handshake: ClientHello, len=55
		{0x03, 0x03},                   // version TLS 1.2
		make([]byte, 32),               // random
		{0x00},                         // session_id len=0
		{0x00, 0x02, 0x00, 0x2f},       // cipher_suites: AES128-SHA
		{0x01, 0x00},                   // compression_methods: null
		{0x00, 0x0c},                   // extensions len=12
		{0x00, 0x17, 0x00, 0x00},       // extended_master_secret
		{0xff, 0x01, 0x00, 0x01, 0x00}, // renegotiation_info empty
		{0x00, 0x0b, 0x00, 0x01, 0x00}, // ec_point_formats
	}, nil)
	sni, err := parseSNI(b)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sni != "" {
		t.Fatalf("want empty SNI, got %q", sni)
	}
}

func TestPrefixConn_ReadDrainsPrefix(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() { c2.Write([]byte("WORLD")); c2.Close() }()
	pc := &prefixConn{prefix: []byte("HELLO "), Conn: c1}
	buf := make([]byte, 11)
	n, _ := pc.Read(buf[:6])
	if string(buf[:n]) != "HELLO " {
		t.Fatalf("prefix: %q", buf[:n])
	}
	n2, _ := pc.Read(buf[6:])
	if string(buf[6:6+n2]) != "WORLD" {
		t.Fatalf("suffix: %q", buf[6:6+n2])
	}
}
