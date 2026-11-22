package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/josegonzalez/litterrobot4-cli/internal/config"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/cloud"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/esphome"
)

const cloudIntro = `Litter-Robot 4 units with stock firmware have no local API. The Whisker
app, Home Assistant and this tool all control them through the Whisker
cloud, so they need your Whisker account (the one you use in the app).
`

const esphomePasswordHelp = `This robot runs ESPHome firmware. Its web server password is set in the
firmware's YAML config (web_server: auth:). The example config uses the
username "user". Leave the password empty if auth is turned off.
`

type setupper struct {
	p        *prompter
	cfg      *config.Config
	insecure bool

	login         func(ctx context.Context) error
	listCloud     func(ctx context.Context) ([]cloud.RobotData, error)
	discover      func(ctx context.Context) ([]litterrobot.Device, error)
	verifyESPHome func(ctx context.Context, host, username, password string) error
}

func cmdSetup(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	discoverTimeout := fs.Duration("discover-timeout", 3*time.Second, "how long to search the local network")
	insecure := fs.Bool("insecure-storage", false, "store secrets in the config file instead of the keychain")
	noCloud := fs.Bool("no-cloud", false, "skip the Whisker account")
	noLAN := fs.Bool("no-lan", false, "skip the local network search")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	s := &setupper{p: newPrompter(out), cfg: cfg, insecure: *insecure}
	s.login = func(ctx context.Context) error {
		save, err := s.p.confirm(ctx, "Save your password too, so the tool can sign in again on its own when the session expires?", false)
		if err != nil {
			return err
		}
		return login(ctx, s.p, cfg, os.Getenv("LITTERROBOT_EMAIL"), save, *insecure)
	}
	s.listCloud = func(ctx context.Context) ([]cloud.RobotData, error) {
		client, err := cloudClient(cfg)
		if err != nil {
			return nil, err
		}
		return client.Robots(ctx)
	}
	s.discover = func(ctx context.Context) ([]litterrobot.Device, error) {
		return litterrobot.Discover(ctx, litterrobot.DiscoverOptions{Timeout: *discoverTimeout})
	}
	s.verifyESPHome = func(ctx context.Context, host, username, password string) error {
		c, err := esphome.NewClient(host, username, password)
		if err != nil {
			return err
		}
		return c.Ping(ctx)
	}
	if *noCloud {
		s.listCloud = nil
	}
	if *noLAN {
		s.discover = nil
	}
	return s.run(ctx)
}

func (s *setupper) run(ctx context.Context) error {
	added := 0
	if s.listCloud != nil {
		n, err := s.setupCloud(ctx)
		if err != nil {
			return err
		}
		added += n
	}
	if s.discover != nil {
		n, err := s.setupLocal(ctx)
		if err != nil {
			return err
		}
		added += n
	}

	fmt.Fprintln(s.p.out)
	if added == 0 {
		fmt.Fprintln(s.p.out, "Nothing new to set up.")
	} else {
		fmt.Fprintf(s.p.out, "Set up %d %s.\n", added, plural(added, "robot", "robots"))
	}
	if len(s.cfg.Devices) > 0 {
		fmt.Fprintln(s.p.out, "Run \"litterrobot4-cli status\" to see them.")
	}
	return nil
}

func (s *setupper) setupCloud(ctx context.Context) (int, error) {
	fmt.Fprintln(s.p.out, "== Whisker account ==")
	robots, err := s.listCloud(ctx)
	if errors.Is(err, errNoAccount) {
		fmt.Fprint(s.p.out, cloudIntro)
		var ok bool
		if ok, err = s.p.confirm(ctx, "Sign in now?", true); err != nil || !ok {
			return 0, err
		}
		for attempt := 0; ; attempt++ {
			err = s.login(ctx)
			if err == nil {
				break
			}
			var ae *cloud.AuthError
			if !errors.As(err, &ae) || attempt == 2 {
				return 0, err
			}
			fmt.Fprintf(s.p.out, "%v. Try again.\n", err)
		}
		robots, err = s.listCloud(ctx)
	}
	if err != nil {
		return 0, err
	}
	if len(robots) == 0 {
		fmt.Fprintln(s.p.out, "No Litter-Robot 4 units on this account.")
		return 0, nil
	}

	added := 0
	for _, r := range robots {
		st := r.State()
		if name, _, ok := s.cfg.BySerial(r.Serial()); ok {
			fmt.Fprintf(s.p.out, "%s (serial %s) is already saved as %q.\n", st.Name, st.Serial, name)
			continue
		}
		fmt.Fprintf(s.p.out, "\nFound %q, serial %s, %s.\n", st.Name, st.Serial, strings.ToLower(st.Status.Text))
		def := defaultName(st.Name, st.Serial)
		if _, taken := s.cfg.Devices[def]; taken {
			def = defaultName("", st.Serial)
		}
		name, err := s.askName(ctx, def)
		if err != nil {
			return added, err
		}
		location, err := s.askLocation(ctx, name)
		if err != nil {
			return added, err
		}
		s.cfg.Devices[name] = &config.Device{Backend: config.BackendCloud, Serial: st.Serial, Location: location}
		if err := s.saveConfig(name); err != nil {
			return added, err
		}
		fmt.Fprintf(s.p.out, "Saved %q.\n", name)
		added++
	}
	return added, nil
}

func (s *setupper) setupLocal(ctx context.Context) (int, error) {
	fmt.Fprintln(s.p.out, "\n== Local network ==")
	devices, err := s.discover(ctx)
	if err != nil {
		fmt.Fprintln(s.p.out, "Local search failed:", err)
		return 0, nil
	}
	if len(devices) == 0 {
		fmt.Fprintln(s.p.out, "No robots found on the local network.")
		return 0, nil
	}

	added := 0
	for _, d := range devices {
		switch d.Source {
		case litterrobot.SourceESPHome:
			ok, err := s.setupESPHome(ctx, d)
			if err != nil {
				return added, err
			}
			if ok {
				added++
			}
		default:
			s.noteStock(d)
		}
	}
	return added, nil
}

// noteStock explains a stock robot found on the network.
func (s *setupper) noteStock(d litterrobot.Device) {
	for _, n := range s.cfg.Names() {
		if s.cfg.Devices[n].Host == d.Address {
			fmt.Fprintf(s.p.out, "Stock robot at %s is saved as %q.\n", d.Address, n)
			return
		}
	}
	fmt.Fprintf(s.p.out, "Found a stock robot at %s (%s). It is controlled through the Whisker cloud,\n", d.Address, d.Hostname)
	fmt.Fprintln(s.p.out, "so its address is only a label. To attach it to a saved robot, run:")
	fmt.Fprintf(s.p.out, "  litterrobot4-cli config host <name> %s\n", d.Address)
}

func (s *setupper) setupESPHome(ctx context.Context, d litterrobot.Device) (bool, error) {
	for _, n := range s.cfg.Names() {
		dev := s.cfg.Devices[n]
		if dev.Backend != config.BackendESPHome {
			continue
		}
		macMatch := d.MAC != "" && config.NormalizeMAC(dev.MAC) == config.NormalizeMAC(d.MAC)
		if macMatch && dev.Host != d.Address && dev.Host != d.Hostname {
			fmt.Fprintf(s.p.out, "%q moved from %s to %s; updating.\n", n, dev.Host, d.Address)
			dev.Host = d.Address
			if err := s.cfg.Save(); err != nil {
				return false, err
			}
			return false, nil
		}
		if macMatch || dev.Host == d.Address || dev.Host == d.Hostname {
			fmt.Fprintf(s.p.out, "ESPHome robot at %s is saved as %q.\n", d.Address, n)
			return false, nil
		}
	}

	fmt.Fprintf(s.p.out, "\nFound an ESPHome robot at %s (%s).\n", d.Address, d.Hostname)
	fmt.Fprint(s.p.out, esphomePasswordHelp)
	var username, password string
	for attempt := 0; ; attempt++ {
		u, err := s.p.ask(ctx, fmt.Sprintf("Username [%s]: ", esphome.DefaultUsername))
		if err != nil {
			return false, err
		}
		username = strings.TrimSpace(u)
		if username == "" {
			username = esphome.DefaultUsername
		}
		if password, err = s.p.readSecret(ctx, "Password: "); err != nil {
			return false, err
		}
		err = s.verifyESPHome(ctx, d.Address, username, password)
		if err == nil {
			break
		}
		if attempt == 2 {
			fmt.Fprintf(s.p.out, "Skipping %s: %v\n", d.Address, err)
			return false, nil
		}
		fmt.Fprintf(s.p.out, "%v. Try again.\n", err)
	}

	def := "litter-robot"
	if mac := config.NormalizeMAC(d.MAC); len(mac) >= 6 {
		def += "-" + strings.ToLower(mac[len(mac)-6:])
	}
	name, err := s.askName(ctx, def)
	if err != nil {
		return false, err
	}
	location, err := s.askLocation(ctx, name)
	if err != nil {
		return false, err
	}
	dev := &config.Device{
		Backend:  config.BackendESPHome,
		Host:     d.Address,
		MAC:      config.NormalizeMAC(d.MAC),
		Location: location,
		Username: username,
	}
	note := ""
	if password != "" {
		storage, err := s.cfg.SetSecret(config.DeviceKey(name, dev), password, s.insecure, &dev.Password)
		if err != nil {
			return false, err
		}
		note = fmt.Sprintf(", password in %s", storage)
	}
	s.cfg.Devices[name] = dev
	if err := s.saveConfig(name); err != nil {
		return false, err
	}
	fmt.Fprintf(s.p.out, "Saved %q%s.\n", name, note)
	return true, nil
}

func (s *setupper) saveConfig(name string) error {
	if s.cfg.Default == "" {
		s.cfg.Default = name
	}
	return s.cfg.Save()
}

func (s *setupper) askName(ctx context.Context, def string) (string, error) {
	for {
		name, err := s.p.ask(ctx, fmt.Sprintf("Name for this robot [%s]: ", def))
		if err != nil {
			return "", err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = def
		}
		if err := validName(name); err != nil {
			fmt.Fprintf(s.p.out, "%v.\n", err)
			continue
		}
		if _, ok := s.cfg.Devices[name]; ok {
			fmt.Fprintf(s.p.out, "%q is already used by another robot.\n", name)
			continue
		}
		return name, nil
	}
}

func (s *setupper) askLocation(ctx context.Context, name string) (string, error) {
	for {
		location, err := s.p.ask(ctx, "Location, such as \"Living Room\" (optional): ")
		if err != nil {
			return "", err
		}
		location = strings.TrimSpace(location)
		if err := checkLocation(s.cfg, name, location); err != nil {
			fmt.Fprintf(s.p.out, "%v.\n", err)
			continue
		}
		return location, nil
	}
}

// defaultName turns a robot's app name into a config name, falling back
// to the end of its serial.
func defaultName(appName, serial string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(appName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	if n := strings.TrimSuffix(b.String(), "-"); n != "" {
		return n
	}
	if len(serial) > 4 {
		serial = serial[len(serial)-4:]
	}
	return "litter-robot-" + strings.ToLower(serial)
}
