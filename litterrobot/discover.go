package litterrobot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

// ESPHomeServiceType is the mDNS service ESPHome devices advertise.
const ESPHomeServiceType = "_esphomelib._tcp"

// Discovery sources.
const (
	SourceESPHome = "esphome"
	SourceLAN     = "lan"
)

// Device is a robot found on the local network.
type Device struct {
	Address  string `json:"address"`
	Hostname string `json:"hostname,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Port     int    `json:"port,omitempty"`
	// Source is SourceESPHome for robots running ESPHome firmware
	// (found over mDNS) and SourceLAN for stock robots found by their
	// DHCP hostname.
	Source string            `json:"source"`
	TXT    map[string]string `json:"txt,omitempty"`
}

// DiscoverOptions controls discovery.
type DiscoverOptions struct {
	// Timeout bounds the whole discovery. Defaults to 3 seconds.
	Timeout time.Duration
	// Interface limits mDNS and the LAN scan to one interface.
	Interface *net.Interface
	// SkipLAN disables the reverse DNS scan for stock robots.
	SkipLAN bool
	// Resolver is used for reverse lookups. net.DefaultResolver when nil.
	Resolver *net.Resolver
}

// IsRobotHostname reports whether a hostname looks like a Litter-Robot 4.
// Stock robots register "Litter-Robot4" over DHCP, and the ESPHome
// firmware uses "litter-robot4-<mac suffix>".
func IsRobotHostname(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "litter-robot") || strings.Contains(n, "litterrobot")
}

// Discover finds robots on the local network: ESPHome robots over mDNS,
// and stock robots by reverse DNS lookups of every address on the local
// subnets, which finds the DHCP hostname the robot registers with the
// router. Stock robots that did not register a hostname are not found.
func Discover(ctx context.Context, opts DiscoverOptions) ([]Device, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout+time.Second)
	defer cancel()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		found   = map[string]Device{}
		mdnsErr error
	)
	add := func(d Device) {
		mu.Lock()
		defer mu.Unlock()
		if old, ok := found[d.Address]; ok && old.Source == SourceESPHome {
			return
		}
		found[d.Address] = d
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		devices, err := discoverESPHome(ctx, opts.Timeout, opts.Interface)
		if err != nil {
			mu.Lock()
			mdnsErr = err
			mu.Unlock()
		}
		for _, d := range devices {
			add(d)
		}
	}()
	if !opts.SkipLAN {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, d := range discoverLAN(ctx, opts) {
				add(d)
			}
		}()
	}
	wg.Wait()

	macs := ReadARP()
	devices := make([]Device, 0, len(found))
	for _, d := range found {
		if d.MAC == "" {
			d.MAC = macs[d.Address]
		}
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool {
		return ipLess(devices[i].Address, devices[j].Address)
	})
	if len(devices) == 0 && mdnsErr != nil {
		return nil, mdnsErr
	}
	return devices, nil
}

func discoverESPHome(ctx context.Context, timeout time.Duration, iface *net.Interface) ([]Device, error) {
	entries := make(chan *mdns.ServiceEntry, 32)
	found := map[string]Device{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for e := range entries {
			if e.AddrV4 == nil || !strings.Contains(e.Name, ESPHomeServiceType+".") {
				continue
			}
			host := strings.TrimSuffix(e.Host, ".")
			txt := map[string]string{}
			for _, f := range e.InfoFields {
				k, v, _ := strings.Cut(f, "=")
				txt[k] = v
			}
			name := strings.TrimSuffix(e.Name, "."+ESPHomeServiceType+".local.")
			if !IsRobotHostname(host) && !IsRobotHostname(name) && !IsRobotHostname(txt["project_name"]) {
				continue
			}
			found[e.AddrV4.String()] = Device{
				Address:  e.AddrV4.String(),
				Hostname: host,
				MAC:      formatMAC(txt["mac"]),
				Port:     80,
				Source:   SourceESPHome,
				TXT:      txt,
			}
		}
	}()

	// A single query can miss replies, so send several and merge them.
	const rounds = 3
	var err error
	for i := range rounds {
		params := mdns.DefaultParams(ESPHomeServiceType)
		params.Domain = "local"
		params.Timeout = max(timeout/rounds, 200*time.Millisecond)
		params.Entries = entries
		params.DisableIPv6 = true
		params.Interface = iface
		params.WantUnicastResponse = i%2 == 1
		params.Logger = log.New(io.Discard, "", 0)
		if err = mdns.QueryContext(ctx, params); err != nil || ctx.Err() != nil {
			break
		}
	}
	close(entries)
	<-done

	devices := make([]Device, 0, len(found))
	for _, d := range found {
		devices = append(devices, d)
	}
	if len(devices) > 0 {
		return devices, nil
	}
	return nil, err
}

// discoverLAN reverse resolves every address on the local IPv4 subnets
// and keeps the ones whose name looks like a robot.
func discoverLAN(ctx context.Context, opts DiscoverOptions) []Device {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	ips := localSubnetHosts(opts.Interface)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		found []Device
		sem   = make(chan struct{}, 64)
	)
	for _, ip := range ips {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			names, err := resolver.LookupAddr(ctx, ip)
			if err != nil {
				return
			}
			for _, n := range names {
				if IsRobotHostname(n) {
					mu.Lock()
					found = append(found, Device{Address: ip, Hostname: strings.TrimSuffix(n, "."), Source: SourceLAN})
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return found
}

// localSubnetHosts lists the host addresses of the local IPv4 subnets.
// Subnets larger than /22 are narrowed to the /24 around the local
// address to keep the scan short.
func localSubnetHosts(only *net.Interface) []string {
	var ifaces []net.Interface
	if only != nil {
		ifaces = []net.Interface{*only}
	} else {
		var err error
		if ifaces, err = net.Interfaces(); err != nil {
			return nil
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip virtual bridges and tunnels used by VMs and VPNs.
		if strings.HasPrefix(iface.Name, "bridge") || strings.HasPrefix(iface.Name, "utun") ||
			strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			for _, h := range SubnetHosts(ipn) {
				if !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
	}
	return out
}

// SubnetHosts returns the host addresses in an IPv4 network, excluding
// the network, broadcast and local addresses. Networks larger than /22
// are narrowed to the /24 around the address.
func SubnetHosts(ipn *net.IPNet) []string {
	ip4 := ipn.IP.To4()
	if ip4 == nil || ip4.IsLinkLocalUnicast() {
		return nil
	}
	ones, bits := ipn.Mask.Size()
	if bits != 32 {
		return nil
	}
	if ones < 22 {
		ones = 24
	}
	if ones > 30 {
		return nil
	}
	mask := net.CIDRMask(ones, 32)
	self := binary.BigEndian.Uint32(ip4)
	network := self & binary.BigEndian.Uint32(mask)
	size := uint32(1) << (32 - ones)
	hosts := make([]string, 0, size-2)
	for i := uint32(1); i < size-1; i++ {
		n := network + i
		if n == self {
			continue
		}
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, n)
		hosts = append(hosts, b.String())
	}
	return hosts
}

// ReadARP returns the system ARP table as IP to MAC. It is best effort:
// some systems hide the table from unprivileged processes.
func ReadARP() map[string]string {
	if runtime.GOOS == "linux" {
		if f, err := os.Open("/proc/net/arp"); err == nil {
			defer f.Close()
			return ParseProcARP(f)
		}
	}
	out, err := exec.Command("arp", "-an").Output()
	if err != nil {
		return map[string]string{}
	}
	return ParseARPOutput(bytes.NewReader(out))
}

// ParseProcARP parses Linux's /proc/net/arp.
func ParseProcARP(r io.Reader) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(r)
	s.Scan() // header
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 4 || f[3] == "00:00:00:00:00:00" {
			continue
		}
		out[f[0]] = strings.ToUpper(f[3])
	}
	return out
}

// ParseARPOutput parses "arp -an" output from macOS, the BSDs and
// Windows ("arp -a").
func ParseARPOutput(r io.Reader) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		f := strings.Fields(s.Text())
		switch {
		// ? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
		case len(f) >= 4 && f[2] == "at" && strings.HasPrefix(f[1], "("):
			ip := strings.Trim(f[1], "()")
			if mac := normalizeMAC(f[3]); mac != "" {
				out[ip] = mac
			}
		// 192.168.1.1  aa-bb-cc-dd-ee-ff  dynamic
		case len(f) >= 2 && net.ParseIP(f[0]) != nil:
			if mac := normalizeMAC(f[1]); mac != "" {
				out[f[0]] = mac
			}
		}
	}
	return out
}

// normalizeMAC returns an uppercase colon separated MAC, padding
// single digit octets as macOS prints them, or "" if s is not a MAC.
func normalizeMAC(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == '-' })
	if len(parts) != 6 {
		return ""
	}
	for i, p := range parts {
		if len(p) == 1 {
			p = "0" + p
		}
		if len(p) != 2 {
			return ""
		}
		parts[i] = strings.ToUpper(p)
	}
	mac := strings.Join(parts, ":")
	if _, err := net.ParseMAC(mac); err != nil || mac == "FF:FF:FF:FF:FF:FF" {
		return ""
	}
	return mac
}

// formatMAC formats a bare hex MAC (as ESPHome's TXT record has it).
func formatMAC(s string) string {
	s = strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(s))
	if len(s) != 12 {
		return ""
	}
	return s[0:2] + ":" + s[2:4] + ":" + s[4:6] + ":" + s[6:8] + ":" + s[8:10] + ":" + s[10:12]
}

func ipLess(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if x == nil || y == nil {
		return a < b
	}
	return bytes.Compare(x, y) < 0
}
