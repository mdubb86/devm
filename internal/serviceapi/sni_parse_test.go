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

		// Non-ClientHello handshake (ServerHello type 0x02).
		"non_clienthello_handshake": {
			0x16, 0x03, 0x01, 0x00, 0x04,
			0x02, 0x00, 0x00, 0x00,
		},
		// Handshake declares helloLen=10, hello body is cut off before
		// version/random/sid_len can be read.
		"truncated_hello": {
			0x16, 0x03, 0x01, 0x00, 0x0e,
			0x01, 0x00, 0x00, 0x0a,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		},
		// compLen=5 but zero compression bytes follow — must error,
		// not silently return ("", nil).
		"truncated_compression": bytes.Join([][]byte{
			{0x16, 0x03, 0x01, 0x00, 0x2c}, // record len=44
			{0x01, 0x00, 0x00, 0x28},       // helloLen=40
			{0x03, 0x03},
			make([]byte, 32),
			{0x00},                   // sid_len=0
			{0x00, 0x02, 0x00, 0x2f}, // ciphers len=2 + AES128-SHA
			{0x05},                   // comp_len=5, nothing follows
		}, nil),
		// cipherLen=100 but only 2 cipher bytes present.
		"truncated_ciphers": bytes.Join([][]byte{
			{0x16, 0x03, 0x01, 0x00, 0x2b}, // record len=43
			{0x01, 0x00, 0x00, 0x27},       // helloLen=39
			{0x03, 0x03},
			make([]byte, 32),
			{0x00},                   // sid_len=0
			{0x00, 0x64, 0x00, 0x2f}, // ciphers len=100, 2 bytes present
		}, nil),
		// ext_total=100 but no extension bytes present.
		"ext_overrun": bytes.Join([][]byte{
			{0x16, 0x03, 0x01, 0x00, 0x2c}, // record len=44
			{0x01, 0x00, 0x00, 0x28},       // helloLen=40
			{0x03, 0x03},
			make([]byte, 32),
			{0x00},       // sid_len=0
			{0x00, 0x00}, // ciphers len=0
			{0x00},       // comp_len=0
			{0x00, 0x64}, // ext_total=100
		}, nil),
		// ext_total=5: one empty extension (4 bytes) plus 1 leftover
		// byte — the loop must error on a stray partial extension
		// header rather than silently exit.
		"ext_leftover": bytes.Join([][]byte{
			{0x16, 0x03, 0x01, 0x00, 0x31}, // record len=49
			{0x01, 0x00, 0x00, 0x2d},       // helloLen=45
			{0x03, 0x03},
			make([]byte, 32),
			{0x00},                         // sid_len=0
			{0x00, 0x00},                   // ciphers len=0
			{0x00},                         // comp_len=0
			{0x00, 0x05},                   // ext_total=5
			{0x00, 0x17, 0x00, 0x00, 0xff}, // 1 ext (4B) + 1 stray
		}, nil),
	}
	for name, b := range cases {
		if _, err := parseSNI(b); err == nil {
			t.Fatalf("%s: want error, got nil", name)
		}
	}
}

func TestParseSNI_MissingExtension(t *testing.T) {
	// A valid ClientHello without the SNI extension yields "" and nil.
	// Lengths: 3 extensions totalling 4+5+5=14 bytes; handshake body =
	// 2+32+1+2+2+1+1+2+14 = 57; record body = 4+57 = 61.
	b := bytes.Join([][]byte{
		{0x16, 0x03, 0x01, 0x00, 0x3d}, // record: handshake, TLS 1.0, len=61
		{0x01, 0x00, 0x00, 0x39},       // handshake: ClientHello, len=57
		{0x03, 0x03},                   // version TLS 1.2
		make([]byte, 32),               // random
		{0x00},                         // session_id len=0
		{0x00, 0x02, 0x00, 0x2f},       // cipher_suites: AES128-SHA
		{0x01, 0x00},                   // compression_methods: null
		{0x00, 0x0e},                   // extensions len=14
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

func TestParseSNI_SkipsNonHostnameNameType(t *testing.T) {
	// server_name extension with two entries: a non-hostname type (0x01)
	// followed by a hostname entry (0x00). The parser must skip the
	// first and return the second's name.
	//   list:   [1+2+5 "dummy"] + [1+2+16 "api.sibling.test"] = 27 B
	//   extBody: list_len(2) + list = 29 B
	//   ext:    type(2) + len(2) + body(29) = 33 B
	//   hello:  2+32+1+2+1+2+33 = 73 B
	//   record: 4+73 = 77 B
	b := bytes.Join([][]byte{
		{0x16, 0x03, 0x01, 0x00, 0x4d}, // record len=77
		{0x01, 0x00, 0x00, 0x49},       // helloLen=73
		{0x03, 0x03},
		make([]byte, 32),
		{0x00},                              // sid_len=0
		{0x00, 0x00},                        // cipher_suites len=0
		{0x00},                              // comp_len=0
		{0x00, 0x21},                        // ext_total=33
		{0x00, 0x00, 0x00, 0x1d},            // server_name, len=29
		{0x00, 0x1b},                        // list_len=27
		{0x01, 0x00, 0x05}, []byte("dummy"), // name_type=1 (skip)
		{0x00, 0x00, 0x10}, []byte("api.sibling.test"), // name_type=0
	}, nil)
	got, err := parseSNI(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "api.sibling.test" {
		t.Fatalf("want api.sibling.test, got %q", got)
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

func TestPrefixConn_PartialReadDrainsPrefix(t *testing.T) {
	// Caller asks for fewer bytes than the prefix holds; the remainder
	// must stay in the prefix for subsequent reads. After the prefix is
	// drained, reads must fall through to the underlying Conn.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() { c2.Write([]byte("TAIL")); c2.Close() }()
	pc := &prefixConn{prefix: []byte("ABCDEF"), Conn: c1}
	buf := make([]byte, 10)
	n, _ := pc.Read(buf[:3])
	if string(buf[:n]) != "ABC" {
		t.Fatalf("read 1: %q", buf[:n])
	}
	n2, _ := pc.Read(buf[3:7])
	if string(buf[3:3+n2]) != "DEF" {
		t.Fatalf("read 2: %q", buf[3:3+n2])
	}
	n3, _ := pc.Read(buf[6:])
	if string(buf[6:6+n3]) != "TAIL" {
		t.Fatalf("read 3 (fallthrough): %q", buf[6:6+n3])
	}
}
