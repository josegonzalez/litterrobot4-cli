package litterrobot

import (
	"net"
	"strings"
	"testing"
)

func TestIsRobotHostname(t *testing.T) {
	for _, n := range []string{"Litter-Robot4.", "litter-robot4-a1b2c3.local", "LitterRobot4"} {
		if !IsRobotHostname(n) {
			t.Errorf("%q should match", n)
		}
	}
	for _, n := range []string{"RabbitAir-8CAAB5AAE3A4.", "iPhone.", ""} {
		if IsRobotHostname(n) {
			t.Errorf("%q should not match", n)
		}
	}
}

func TestParseProcARP(t *testing.T) {
	in := `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.50    0x1         0x2         7c:9e:bd:01:02:03     *        eth0
192.168.1.51    0x1         0x0         00:00:00:00:00:00     *        eth0
`
	got := ParseProcARP(strings.NewReader(in))
	if len(got) != 1 || got["192.168.1.50"] != "7C:9E:BD:01:02:03" {
		t.Errorf("got %v", got)
	}
}

func TestParseARPOutput(t *testing.T) {
	in := `? (192.168.1.1) at 0:11:22:33:44:5 on en0 ifscope [ethernet]
? (192.168.1.51) at (incomplete) on en0 ifscope [ethernet]
? (192.168.1.255) at ff:ff:ff:ff:ff:ff on en0 ifscope [ethernet]
  192.168.1.50         7c-9e-bd-01-02-03     dynamic
`
	got := ParseARPOutput(strings.NewReader(in))
	want := map[string]string{"192.168.1.1": "00:11:22:33:44:05", "192.168.1.50": "7C:9E:BD:01:02:03"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSubnetHosts(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.1.0/24")
	n.IP = net.ParseIP("192.168.1.232").To4()
	hosts := SubnetHosts(n)
	if len(hosts) != 253 || hosts[0] != "192.168.1.1" || hosts[len(hosts)-1] != "192.168.1.254" {
		t.Fatalf("got %d hosts, first %s last %s", len(hosts), hosts[0], hosts[len(hosts)-1])
	}
	for _, h := range hosts {
		if h == "192.168.1.232" {
			t.Error("local address included")
		}
	}

	// Big networks are narrowed to the local /24.
	_, big, _ := net.ParseCIDR("10.0.0.0/8")
	big.IP = net.ParseIP("10.20.30.40").To4()
	hosts = SubnetHosts(big)
	if len(hosts) != 253 || hosts[0] != "10.20.30.1" {
		t.Errorf("narrowed: %d hosts, first %s", len(hosts), hosts[0])
	}

	_, ll, _ := net.ParseCIDR("169.254.0.0/16")
	if got := SubnetHosts(ll); len(got) != 0 {
		t.Errorf("link local: %d hosts", len(got))
	}
}

func TestFormatMAC(t *testing.T) {
	if got := formatMAC("7c9ebd010203"); got != "7C:9E:BD:01:02:03" {
		t.Errorf("got %q", got)
	}
	if got := formatMAC("nope"); got != "" {
		t.Errorf("got %q", got)
	}
}
