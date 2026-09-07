//go:build windows

package daemon

import "testing"

const netstatSample = `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1120
  TCP    0.0.0.0:18789          0.0.0.0:0              LISTENING       9001
  TCP    127.0.0.1:18789        127.0.0.1:54321        ESTABLISHED     9001
  TCP    127.0.0.1:54321        127.0.0.1:18789        ESTABLISHED     7777
  TCP    [::]:18789             [::]:0                 LISTENING       9001
  TCP    [::]:445               [::]:0                 LISTENING       4
  UDP    0.0.0.0:18789          *:*                                    5555
`

func TestParseNetstatListeners(t *testing.T) {
	got := parseNetstatListeners(netstatSample, 18789)
	if len(got) != 1 || got[0] != 9001 {
		t.Fatalf("parseNetstatListeners = %v, want [9001]", got)
	}
}

// The gateway kills whatever holds its port, so a false positive here means
// killing an unrelated process. Everything that merely mentions the port —
// an outbound connection to it, a UDP socket on it — must be ignored.
func TestParseNetstatListenersIgnoresNonListeners(t *testing.T) {
	// 7777 has a *foreign* address on 18789; 5555 is UDP.
	for _, pid := range parseNetstatListeners(netstatSample, 18789) {
		if pid == 7777 || pid == 5555 {
			t.Errorf("pid %d is not listening on 18789 and must not be returned", pid)
		}
	}
}

func TestParseNetstatListenersPortIsNotAPrefixMatch(t *testing.T) {
	// 1878 must not match the row for 18789.
	if got := parseNetstatListeners(netstatSample, 1878); len(got) != 0 {
		t.Errorf("parseNetstatListeners(.., 1878) = %v, want none", got)
	}
	if got := parseNetstatListeners(netstatSample, 8789); len(got) != 0 {
		t.Errorf("parseNetstatListeners(.., 8789) = %v, want none", got)
	}
}

func TestParseNetstatListenersEmpty(t *testing.T) {
	if got := parseNetstatListeners("", 18789); len(got) != 0 {
		t.Errorf("empty output = %v, want none", got)
	}
	if got := parseNetstatListeners(netstatSample, 65000); len(got) != 0 {
		t.Errorf("unused port = %v, want none", got)
	}
}
