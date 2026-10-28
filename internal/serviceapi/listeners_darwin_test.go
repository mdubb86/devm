package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// netstatFixtureHostSSHOnly reproduces the Remote-Login-enabled
// state: host sshd on *:22 accepts on every lo0 alias, no project
// has bound 127.42.0.5:22 specifically.
const netstatFixtureHostSSHOnly = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  *.22                   *.*                    LISTEN
tcp4       0      0  127.0.0.1.631          *.*                    LISTEN
tcp4       0      0  10.0.0.5.49200         140.82.112.22.443      ESTABLISHED
`

// netstatFixtureSpecificBind reproduces a softnet actually holding
// 127.42.0.5: it has a specific-address LISTEN on :22, :80, :443.
const netstatFixtureSpecificBind = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  127.42.0.5.22          *.*                    LISTEN
tcp4       0      0  127.42.0.5.80          *.*                    LISTEN
tcp4       0      0  127.42.0.5.443         *.*                    LISTEN
tcp4       0      0  127.42.0.6.22          *.*                    LISTEN
`

// netstatFixtureMixed combines both: host sshd on *:22 PLUS a softnet
// on 127.42.0.5. 127.42.0.5 is held (specific bind on 22); 127.42.0.6
// is not (only the wildcard accepts).
const netstatFixtureMixed = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  *.22                   *.*                    LISTEN
tcp4       0      0  127.42.0.5.22          *.*                    LISTEN
tcp4       0      0  127.42.0.5.80          *.*                    LISTEN
`

func TestParseNetstatListeners_WildcardIsIgnored(t *testing.T) {
	ports := parseNetstatListeners([]byte(netstatFixtureHostSSHOnly), "127.42.0.5")
	assert.Empty(t, ports, "wildcard *.22 must not count as a specific bind on 127.42.0.5")
}

func TestParseNetstatListeners_SpecificBindFound(t *testing.T) {
	ports := parseNetstatListeners([]byte(netstatFixtureSpecificBind), "127.42.0.5")
	assert.ElementsMatch(t, []int{22, 80, 443}, ports)
}

func TestParseNetstatListeners_OtherIPsNotReturned(t *testing.T) {
	ports := parseNetstatListeners([]byte(netstatFixtureSpecificBind), "127.42.0.7")
	assert.Empty(t, ports)
}

func TestParseNetstatListeners_MixedWildcardAndSpecific(t *testing.T) {
	ports5 := parseNetstatListeners([]byte(netstatFixtureMixed), "127.42.0.5")
	assert.ElementsMatch(t, []int{22, 80}, ports5, "specific bind on 127.42.0.5 must be found despite the wildcard")

	ports6 := parseNetstatListeners([]byte(netstatFixtureMixed), "127.42.0.6")
	assert.Empty(t, ports6, "no specific bind on 127.42.0.6; wildcard *.22 must not count")
}

func TestParseNetstatListeners_SkipsNonListenRows(t *testing.T) {
	fixture := `Active Internet connections
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  127.42.0.5.22          10.0.0.1.49200         ESTABLISHED
tcp4       0      0  127.42.0.5.80          *.*                    LISTEN
`
	ports := parseNetstatListeners([]byte(fixture), "127.42.0.5")
	assert.ElementsMatch(t, []int{80}, ports, "ESTABLISHED row must not count as a listener")
}

func TestParseNetstatListeners_SkipsIPv6Rows(t *testing.T) {
	// `-f inet` filters IPv6 out at the netstat level, but defensively
	// confirm the parser only trusts tcp4.
	fixture := `Active Internet connections
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp6       0      0  *.22                   *.*                    LISTEN
tcp46      0      0  127.42.0.5.22          *.*                    LISTEN
`
	ports := parseNetstatListeners([]byte(fixture), "127.42.0.5")
	assert.Empty(t, ports, "non-tcp4 rows must not count")
}

func TestSplitLocalAddr(t *testing.T) {
	cases := []struct {
		in     string
		wantIP string
		wantP  int
		wantOk bool
	}{
		{"127.42.0.5.22", "127.42.0.5", 22, true},
		{"*.22", "*", 22, true},
		{"127.0.0.1.631", "127.0.0.1", 631, true},
		{"garbage", "", 0, false},
		{"", "", 0, false},
		{".22", "", 0, false},
		{"127.0.0.1.", "", 0, false},
		{"127.0.0.1.abc", "", 0, false},
	}
	for _, c := range cases {
		gotIP, gotP, gotOk := splitLocalAddr(c.in)
		assert.Equal(t, c.wantOk, gotOk, "ok for %q", c.in)
		if c.wantOk {
			assert.Equal(t, c.wantIP, gotIP, "ip for %q", c.in)
			assert.Equal(t, c.wantP, gotP, "port for %q", c.in)
		}
	}
}
