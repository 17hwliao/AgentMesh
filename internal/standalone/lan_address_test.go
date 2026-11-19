package standalone

import (
	"net/netip"
	"testing"
)

func TestValidateLANAddress(t *testing.T) {
	locals := []netip.Addr{netip.MustParseAddr("192.168.4.22"), netip.MustParseAddr("8.8.8.8")}
	for _, test := range []struct {
		address string
		valid   bool
	}{
		{"192.168.4.22:18080", true},
		{"192.168.4.23:18080", false},
		{"192.168.4.22:0", false},
		{"192.168.4.22:65536", false},
		{"127.0.0.1:18080", false},
		{"0.0.0.0:18080", false},
		{"8.8.8.8:18080", false},
		{"[::]:18080", false},
		{"[fd00::1]:18080", false},
		{"[::ffff:192.168.4.22]:18080", false},
		{"example.test:18080", false},
	} {
		t.Run(test.address, func(t *testing.T) {
			if err := validateLANAddress(test.address, locals); (err == nil) != test.valid {
				t.Fatalf("address %s: want valid=%v, got %v", test.address, test.valid, err)
			}
		})
	}
}
