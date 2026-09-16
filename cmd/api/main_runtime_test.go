package main

import "testing"

func TestValidateServerAddressKeepsDefaultLoopbackBoundary(t *testing.T) {
	if err := validateServerAddress("127.0.0.1:18080", false); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
	if err := validateServerAddress("0.0.0.0:18080", false); err == nil {
		t.Fatal("wildcard binding was accepted without explicit container opt-in")
	}
	if err := validateServerAddress("0.0.0.0:18080", true); err != nil {
		t.Fatalf("container wildcard binding rejected: %v", err)
	}
	if err := validateServerAddress("192.168.1.10:18080", true); err == nil {
		t.Fatal("LAN binding was accepted")
	}
}
