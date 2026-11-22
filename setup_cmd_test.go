package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/josegonzalez/litterrobot4-cli/internal/config"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/cloud"
)

func newTestSetupper(t *testing.T, input string) (*setupper, *bytes.Buffer) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	p := &prompter{in: bufio.NewReader(strings.NewReader(input)), out: out}
	p.secret = p.ask
	return &setupper{p: p, cfg: cfg}, out
}

var cloudRobots = []cloud.RobotData{
	{"serial": "LR4C000001", "name": "Living Room Robot", "isOnline": true, "unitPowerStatus": "ON", "robotStatus": "ROBOT_IDLE"},
	{"serial": "LR4C000002", "name": "Living Room Robot", "isOnline": true, "unitPowerStatus": "ON", "robotStatus": "ROBOT_IDLE"},
}

func TestSetupSignsInAndSavesCloudRobots(t *testing.T) {
	// Sign in? yes. Robot 1: default name, location. Robot 2: the default
	// name is taken, so it falls back to the serial; reuse of the first
	// location is rejected, then a new one is given.
	s, out := newTestSetupper(t, "\n\nLiving Room\n\nLiving Room\nKitchen\n")
	signedIn := false
	s.login = func(ctx context.Context) error {
		signedIn = true
		s.cfg.Account = &config.Account{Email: "cat@example.com"}
		return nil
	}
	s.listCloud = func(ctx context.Context) ([]cloud.RobotData, error) {
		if !signedIn {
			return nil, errNoAccount
		}
		return cloudRobots, nil
	}

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, _ := config.Load()
	a, b := cfg.Devices["living-room-robot"], cfg.Devices["litter-robot-0002"]
	if a == nil || b == nil {
		t.Fatalf("saved: %v\n%s", cfg.Names(), out)
	}
	if a.Serial != "LR4C000001" || a.Location != "Living Room" || a.Backend != config.BackendCloud {
		t.Errorf("first: %+v", a)
	}
	if b.Serial != "LR4C000002" || b.Location != "Kitchen" {
		t.Errorf("second: %+v", b)
	}
	if cfg.Default != "living-room-robot" {
		t.Errorf("default = %q", cfg.Default)
	}
	if !strings.Contains(out.String(), "already used") || !strings.Contains(out.String(), "Set up 2 robots") {
		t.Errorf("output:\n%s", out)
	}
}

func TestSetupSkipsSavedCloudRobots(t *testing.T) {
	s, out := newTestSetupper(t, "")
	s.cfg.Account = &config.Account{Email: "cat@example.com"}
	s.cfg.Devices["one"] = &config.Device{Backend: config.BackendCloud, Serial: "LR4C000001"}
	s.cfg.Devices["two"] = &config.Device{Backend: config.BackendCloud, Serial: "LR4C000002"}
	s.listCloud = func(ctx context.Context) ([]cloud.RobotData, error) { return cloudRobots, nil }
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), `already saved as "one"`) || !strings.Contains(out.String(), "Nothing new") {
		t.Errorf("output:\n%s", out)
	}
}

func TestSetupESPHomeRetriesPassword(t *testing.T) {
	// Username default, wrong password, then default username and the
	// right password, default name, no location.
	s, out := newTestSetupper(t, "\nwrong\n\nright\n\n\n")
	s.discover = func(ctx context.Context) ([]litterrobot.Device, error) {
		return []litterrobot.Device{
			{Address: "192.168.1.60", Hostname: "litter-robot4-010203.local", MAC: "7C:9E:BD:01:02:03", Source: litterrobot.SourceESPHome},
			{Address: "192.168.1.50", Hostname: "Litter-Robot4", Source: litterrobot.SourceLAN},
		}, nil
	}
	s.verifyESPHome = func(ctx context.Context, host, username, password string) error {
		if username != "user" || password != "right" {
			return errors.New("esphome: the device rejected the web server username or password")
		}
		return nil
	}
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, _ := config.Load()
	d := cfg.Devices["litter-robot-010203"]
	if d == nil || d.Backend != config.BackendESPHome || d.Host != "192.168.1.60" || d.MAC != "7C9EBD010203" || d.Username != "user" {
		t.Fatalf("saved: %+v\n%s", d, out)
	}
	if pw, s, err := cfg.Secret(config.DeviceKey("litter-robot-010203", d), d.Password); pw != "right" || s != config.StorageKeychain || err != nil {
		t.Errorf("password: %q %v %v", pw, s, err)
	}
	if !strings.Contains(out.String(), "config host <name> 192.168.1.50") {
		t.Errorf("stock robot note missing:\n%s", out)
	}
}

func TestSetupUpdatesMovedESPHomeRobot(t *testing.T) {
	s, out := newTestSetupper(t, "")
	s.cfg.Devices["basement"] = &config.Device{Backend: config.BackendESPHome, Host: "192.168.1.60", MAC: "7C9EBD010203"}
	s.discover = func(ctx context.Context) ([]litterrobot.Device, error) {
		return []litterrobot.Device{{Address: "192.168.1.61", MAC: "7C:9E:BD:01:02:03", Source: litterrobot.SourceESPHome}}, nil
	}
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, _ := config.Load()
	if got := cfg.Devices["basement"].Host; got != "192.168.1.61" {
		t.Errorf("host = %q\n%s", got, out)
	}
}

func TestDefaultName(t *testing.T) {
	cases := []struct{ name, serial, want string }{
		{"Living Room Robot", "LR4C000001", "living-room-robot"},
		{"  Bob's  Box!! ", "LR4C000001", "bob-s-box"},
		{"", "LR4C00ABCD", "litter-robot-abcd"},
		{"🐈", "LR4C000009", "litter-robot-0009"},
	}
	for _, c := range cases {
		if got := defaultName(c.name, c.serial); got != c.want {
			t.Errorf("defaultName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
