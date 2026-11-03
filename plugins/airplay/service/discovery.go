//go:build airplay

package main

import (
	"context"
	"errors"
	"log"
	"net"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// candidate is one network interface with its usable IPv4 addresses.
type candidate struct {
	iface net.Interface
	ips   []net.IP
}

// lanInterface picks the interface an iPhone can reach: Wi-Fi first, then
// wired Ethernet. USB gadget links (usb*, rndis*) and loopback are never
// advertised; with no Wi-Fi/Ethernet address yet the caller waits.
func lanInterface(candidates []candidate) *candidate {
	rank := func(name string) int {
		switch {
		case strings.HasPrefix(name, "wl"):
			return 0
		case strings.HasPrefix(name, "eth"), strings.HasPrefix(name, "en"):
			return 1
		}
		return -1
	}
	usable := slices.DeleteFunc(slices.Clone(candidates), func(c candidate) bool {
		f := c.iface.Flags
		return rank(c.iface.Name) < 0 || f&net.FlagUp == 0 || f&net.FlagMulticast == 0 || len(c.ips) == 0
	})
	sort.SliceStable(usable, func(i, j int) bool { return rank(usable[i].iface.Name) < rank(usable[j].iface.Name) })
	if len(usable) == 0 {
		return nil
	}
	return &usable[0]
}

func systemCandidates() []candidate {
	interfaces, _ := net.Interfaces()
	out := make([]candidate, 0, len(interfaces))
	for _, iface := range interfaces {
		c := candidate{iface: iface}
		addresses, _ := iface.Addrs()
		for _, a := range addresses {
			if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.To4() != nil && !ip.IsLoopback() {
				c.ips = append(c.ips, ip.To4())
			}
		}
		out = append(out, c)
	}
	return out
}

// advertiser publishes the RAOP service shairport describes and follows the
// speaker's Wi-Fi address: at boot Wi-Fi often comes up after the plugin, and
// DHCP can move the address later.
type advertiser struct {
	server *mdns.Server
	zone   *mdns.MDNSService
	iface  string
	ips    string
}

func (a *advertiser) update(name, service string, port int, txt []string) (bool, error) {
	lan := lanInterface(systemCandidates())
	if lan == nil {
		if a.server != nil {
			a.stop()
			log.Print("AirPlay discovery paused: Wi-Fi has no IPv4 address")
		}
		return false, nil
	}
	ips := fmtIPs(lan.ips)
	if a.server != nil && a.iface == lan.iface.Name && a.ips == ips && a.zone.Instance == name && a.zone.Port == port &&
		slices.Equal(a.zone.TXT, txt) {
		return false, nil
	}
	a.stop()
	host := "alice-" + strings.ToLower(strings.Split(name, "@")[0]) + ".local."
	zone, err := mdns.NewMDNSService(name, service, "local.", host, port, lan.ips, txt)
	if err != nil {
		return false, err
	}
	// hashicorp/mdns ignores a failed IPv4 socket and keeps running on IPv6
	// only; check the port ourselves so the log says why an iPhone sees nothing.
	if probe, err := net.ListenMulticastUDP("udp4", &lan.iface, mdnsGroup); err != nil {
		log.Printf("AirPlay discovery: cannot open mDNS port 5353 on %s: %v", lan.iface.Name, err)
	} else {
		probe.Close()
	}
	iface := lan.iface
	server, err := mdns.NewServer(&mdns.Config{Zone: zone, Iface: &iface})
	if err != nil {
		return false, err
	}
	a.server, a.zone, a.iface, a.ips = server, zone, lan.iface.Name, ips
	log.Printf("AirPlay discovery: %s on %s (%s)", name, lan.iface.Name, ips)
	go announce(zone, iface, false)
	return true, nil
}

func (a *advertiser) stop() {
	if a.server == nil {
		return
	}
	announce(a.zone, *mustInterface(a.iface), true)
	a.server.Shutdown()
	a.server = nil
}

func mustInterface(name string) *net.Interface {
	if iface, err := net.InterfaceByName(name); err == nil {
		return iface
	}
	return &net.Interface{Name: name}
}

func fmtIPs(ips []net.IP) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ",")
}

// announce multicasts the service records (RFC 6762 §8.3) so an iPhone that
// already has the AirPlay menu open sees the speaker without a new query.
// goodbye sends the same records with TTL 0 so a stopped speaker disappears.
func announce(zone *mdns.MDNSService, iface net.Interface, goodbye bool) {
	name := dns.Fqdn(strings.Trim(zone.Service, ".") + "." + strings.Trim(zone.Domain, "."))
	records := zone.Records(dns.Question{Name: name, Qtype: dns.TypePTR, Qclass: dns.ClassINET})
	if len(records) == 0 {
		return
	}
	if goodbye {
		for _, r := range records {
			r.Header().Ttl = 0
		}
	}
	msg := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true, Authoritative: true}, Compress: true, Answer: records}
	packet, err := msg.Pack()
	if err != nil {
		return
	}
	delays := []time.Duration{0, time.Second, 2 * time.Second}
	if goodbye {
		delays = delays[:1]
	}
	for _, delay := range delays {
		time.Sleep(delay)
		if err := sendMulticast(packet, iface); err != nil {
			log.Printf("AirPlay discovery: announcement on %s failed: %v", iface.Name, err)
			return
		}
	}
}

// sendMulticast sends from port 5353, as RFC 6762 requires for responses.
// The socket lives only for one packet so it never takes queries away from
// the responder bound to the same port.
func sendMulticast(packet []byte, iface net.Interface) error {
	config := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var opt error
		err := c.Control(func(fd uintptr) {
			opt = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
		return errors.Join(err, opt)
	}}
	conn, err := config.ListenPacket(context.Background(), "udp4", ":5353")
	if err != nil {
		return err
	}
	defer conn.Close()
	pc := ipv4.NewPacketConn(conn)
	if err := pc.SetMulticastInterface(&iface); err != nil {
		return err
	}
	_ = pc.SetMulticastTTL(255)
	_, err = conn.WriteTo(packet, mdnsGroup)
	return err
}
