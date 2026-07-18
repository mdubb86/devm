package softnet

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogf(t *testing.T) {
	var buf bytes.Buffer
	old := logOut
	logOut = &buf
	defer func() { logOut = old }()
	logf("hello %d", 7)
	if !strings.Contains(buf.String(), "[softnet] hello 7") {
		t.Fatalf("got %q", buf.String())
	}
}
