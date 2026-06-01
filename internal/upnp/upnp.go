// Package upnp adds/removes router port mappings via UPnP-IGD, so game servers
// can be reachable from the internet without manual port forwarding. It degrades
// gracefully: if no IGD is found (router has UPnP off, or none present), Discover
// returns an error and the caller just shows the manual-forwarding helper.
package upnp

import (
	"fmt"
	"net"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

// igd is the small slice of the WAN connection service we use; all three goupnp
// client types (WANIPConnection2/1, WANPPPConnection1) satisfy it.
type igd interface {
	AddPortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string, NewInternalPort uint16, NewInternalClient string, NewEnabled bool, NewPortMappingDescription string, NewLeaseDuration uint32) error
	DeletePortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string) error
	GetExternalIPAddress() (NewExternalIPAddress string, err error)
}

// Client is a discovered IGD plus the LAN IP mappings should point at.
type Client struct {
	dev     igd
	localIP string
}

// Discover finds an Internet Gateway Device on the LAN. Tries the common WAN
// connection service variants in order.
func Discover() (*Client, error) {
	ip := localIP()
	if c2, _, err := internetgateway2.NewWANIPConnection2Clients(); err == nil && len(c2) > 0 {
		return &Client{dev: c2[0], localIP: ip}, nil
	}
	if c1, _, err := internetgateway2.NewWANIPConnection1Clients(); err == nil && len(c1) > 0 {
		return &Client{dev: c1[0], localIP: ip}, nil
	}
	if cp, _, err := internetgateway2.NewWANPPPConnection1Clients(); err == nil && len(cp) > 0 {
		return &Client{dev: cp[0], localIP: ip}, nil
	}
	return nil, fmt.Errorf("no UPnP gateway found (router UPnP may be disabled)")
}

// LocalIP is the LAN IP that mappings forward to.
func (c *Client) LocalIP() string { return c.localIP }

// ExternalIP returns the gateway's WAN/public IP.
func (c *Client) ExternalIP() (string, error) { return c.dev.GetExternalIPAddress() }

// AddMapping forwards external port → this host's internal port for the given
// protocol ("tcp"/"udp"). lease is in seconds (0 = permanent; many routers cap
// or reject 0, so callers pass a finite lease and renew).
func (c *Client) AddMapping(port int, protocol, desc string, leaseSeconds uint32) error {
	if c.localIP == "" {
		return fmt.Errorf("could not determine local IP for UPnP mapping")
	}
	p := uint16(port)
	return c.dev.AddPortMapping("", p, normProto(protocol), p, c.localIP, true, desc, leaseSeconds)
}

// DeleteMapping removes a previously added mapping.
func (c *Client) DeleteMapping(port int, protocol string) error {
	return c.dev.DeletePortMapping("", uint16(port), normProto(protocol))
}

func normProto(p string) string {
	if p == "udp" || p == "UDP" {
		return "UDP"
	}
	return "TCP"
}

// localIP returns this host's primary outbound LAN IP (no traffic sent).
func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
