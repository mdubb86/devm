package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseRequest_Bind_OK(t *testing.T) {
	req, err := parseRequest([]byte(`{"op":"bind","ip":"127.42.0.5","port":80,"proto":"tcp"}`))
	assert.NoError(t, err)
	assert.Equal(t, "bind", req.Op)
	assert.Equal(t, "127.42.0.5", req.IP)
	assert.Equal(t, 80, req.Port)
	assert.Equal(t, "tcp", req.Proto)
}

func TestParseRequest_BadJSON_Errors(t *testing.T) {
	_, err := parseRequest([]byte(`{`))
	assert.Error(t, err)
}

func TestParseRequest_UnknownOp_Errors(t *testing.T) {
	_, err := parseRequest([]byte(`{"op":"nuke","ip":"127.42.0.5","port":80,"proto":"tcp"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "op")
}

func TestValidateIPInPool_OK(t *testing.T) {
	for i := 1; i <= 20; i++ {
		ip := "127.42.0." + itoa(i)
		assert.NoError(t, validateIPInPool(ip), "IP %s should be in pool", ip)
	}
}

func TestValidateIPInPool_OutOfRange_Errors(t *testing.T) {
	for _, bad := range []string{"127.42.0.0", "127.42.0.21", "127.42.0.100", "127.0.0.1", "192.168.1.1", "notanip"} {
		err := validateIPInPool(bad)
		assert.Error(t, err, "IP %s should NOT be in pool", bad)
	}
}

// itoa is a local helper to avoid importing strconv in the test file preamble;
// keeps the test focused on the two functions under test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return strings.TrimLeft(string(buf[:]), "\x00")
}
