package main

import "testing"

func TestDaemonRequiresLoopbackListener(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8766", "localhost:8766", "[::1]:8766"} {
		if err := requireLoopbackAddress(address); err != nil {
			t.Errorf("%s rejected: %v", address, err)
		}
	}
	for _, address := range []string{":8766", "0.0.0.0:8766", "192.0.2.10:8766", "bad"} {
		if err := requireLoopbackAddress(address); err == nil {
			t.Errorf("%s accepted", address)
		}
	}
}
