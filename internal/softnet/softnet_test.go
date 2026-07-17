package softnet

import "testing"

func TestRunRejectsMissingFD(t *testing.T) {
	// No --vm-fd and no real socket: Run must return an error, not panic.
	err := Run([]string{"--vm-mac-address", "aa:bb:cc:dd:ee:ff"})
	if err == nil {
		t.Fatal("Run without a valid --vm-fd should error")
	}
}
