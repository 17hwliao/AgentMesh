package standalone

import (
	"errors"
	"net/netip"
)

func validateLANAddress(address string, localAddresses []netip.Addr) error {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || endpoint.Port() == 0 {
		return errors.New("standalone_lan_address_invalid")
	}
	ip := endpoint.Addr()
	if !ip.Is4() || !ip.IsPrivate() {
		return errors.New("standalone_lan_address_must_be_private_ipv4")
	}
	for _, local := range localAddresses {
		if local.Unmap() == ip {
			return nil
		}
	}
	return errors.New("standalone_lan_address_not_assigned")
}
