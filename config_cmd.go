package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/josegonzalez/litterrobot4-cli/internal/config"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/esphome"
)

const configUsage = `Usage: litterrobot4-cli config <command> [flags]

Commands:
  add <name>       Save a robot (--serial for cloud, --host for ESPHome)
  list             List the account and saved robots
  remove <name>    Remove a saved robot and its secrets
  default <name>   Set the default robot
  location <name> [<location>]
                   Show or set a robot's location ("" clears it)
  host <name> [<host>]
                   Show or set a robot's network address ("" clears it)
  path             Print the config file path
`

func cmdConfig(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, configUsage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "add":
		return cmdConfigAdd(ctx, args[1:], out)
	case "list", "ls":
		return cmdConfigList(args[1:], out)
	case "remove", "rm":
		return cmdConfigRemove(args[1:], out)
	case "default":
		return cmdConfigDefault(args[1:], out)
	case "location":
		return cmdConfigField(args[1:], out, "location")
	case "host":
		return cmdConfigField(args[1:], out, "host")
	case "path":
		p, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, p)
		return nil
	case "-h", "--help", "help":
		fmt.Fprint(out, configUsage)
		return nil
	}
	fmt.Fprint(os.Stderr, configUsage)
	return fmt.Errorf("unknown config command %q", args[0])
}

// parseNamed parses flags that may come before or after a single
// positional name argument.
func parseNamed(fs *flag.FlagSet, args []string) (string, error) {
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if name == "" {
		return "", errors.New("a robot name is required")
	}
	return name, validName(name)
}

func validName(name string) error {
	if strings.ContainsAny(name, " \t\"'") {
		return errors.New("names cannot contain spaces or quotes")
	}
	return nil
}

func cmdConfigAdd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config add", flag.ContinueOnError)
	serial := fs.String("serial", "", "serial of a robot on the Whisker account (stock firmware)")
	host := fs.String("host", "", "robot address; required for ESPHome robots, a label for cloud robots")
	username := fs.String("username", esphome.DefaultUsername, "ESPHome web server username")
	password := fs.String("password", os.Getenv("LITTERROBOT_PASSWORD"), "ESPHome web server password (env LITTERROBOT_PASSWORD)")
	mac := fs.String("mac", "", "robot MAC address")
	location := fs.String("location", "", "where the robot is, such as \"Living Room\" (kept when re-adding)")
	makeDefault := fs.Bool("default", false, "make this the default robot")
	insecure := fs.Bool("insecure-storage", false, "store the password in the config file instead of the keychain")
	noVerify := fs.Bool("no-verify", false, "save without contacting the robot or the Whisker cloud")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: litterrobot4-cli config add <name> --serial <serial> [--host <address>] [flags]")
		fmt.Fprintln(fs.Output(), "       litterrobot4-cli config add <name> --host <address> [--password <password>] [flags]")
		fs.PrintDefaults()
	}

	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if *serial == "" && *host == "" {
		return errors.New("--serial or --host is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dev := &config.Device{Host: *host, MAC: config.NormalizeMAC(*mac), Location: strings.TrimSpace(*location)}
	if *serial != "" {
		dev.Backend = config.BackendCloud
		dev.Serial = strings.TrimSpace(*serial)
		if !*noVerify {
			client, err := cloudClient(cfg)
			if err != nil {
				return err
			}
			if _, err := client.RobotBySerial(ctx, dev.Serial); err != nil {
				return fmt.Errorf("could not find the robot (use --no-verify to skip): %w", err)
			}
		}
		if n, _, ok := cfg.BySerial(dev.Serial); ok && n != name {
			return fmt.Errorf("serial %s is already saved as %q", dev.Serial, n)
		}
	} else {
		dev.Backend = config.BackendESPHome
		dev.Username = *username
		if !*noVerify {
			c, err := esphome.NewClient(*host, *username, *password)
			if err != nil {
				return err
			}
			if err := c.Ping(ctx); err != nil {
				return fmt.Errorf("could not reach the robot (use --no-verify to skip): %w", err)
			}
		}
	}

	if old, ok := cfg.Devices[name]; ok && dev.Location == "" {
		dev.Location = old.Location
	}
	if err := checkLocation(cfg, name, dev.Location); err != nil {
		return err
	}
	if _, ok := cfg.Devices[name]; ok {
		if err := cfg.Remove(name); err != nil {
			return err
		}
	}

	secretNote := ""
	if dev.Backend == config.BackendESPHome && *password != "" {
		storage, err := cfg.SetSecret(config.DeviceKey(name, dev), *password, *insecure, &dev.Password)
		if err != nil {
			return err
		}
		secretNote = fmt.Sprintf(", password in %s", storage)
	}
	cfg.Devices[name] = dev
	if *makeDefault || cfg.Default == "" {
		cfg.Default = name
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %q (%s) to %s%s\n", name, describe(dev), cfg.File(), secretNote)
	return nil
}

func describe(d *config.Device) string {
	if d.Backend == config.BackendCloud {
		return "cloud, serial " + d.Serial
	}
	return "esphome, " + d.Host
}

func cmdConfigList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	type row struct {
		Name     string `json:"name"`
		Location string `json:"location,omitempty"`
		Backend  string `json:"backend"`
		Serial   string `json:"serial,omitempty"`
		Host     string `json:"host,omitempty"`
		MAC      string `json:"mac,omitempty"`
		Secret   string `json:"secret,omitempty"`
		Default  bool   `json:"default"`
	}
	type account struct {
		Email   string `json:"email"`
		Session string `json:"session"`
	}
	var acct *account
	if cfg.Account != nil {
		acct = &account{Email: cfg.Account.Email, Session: "missing"}
		if _, s, err := cfg.Secret(config.RefreshTokenKey(cfg.Account.Email), cfg.Account.RefreshToken); err == nil {
			acct.Session = string(s)
		}
	}
	rows := []row{}
	for _, n := range cfg.Names() {
		d := cfg.Devices[n]
		r := row{Name: n, Location: d.Location, Backend: d.Backend, Serial: d.Serial, Host: d.Host, MAC: d.MAC, Default: n == cfg.Default}
		if d.Backend == config.BackendESPHome {
			r.Secret = "none"
			if _, s, err := cfg.Secret(config.DeviceKey(n, d), d.Password); err == nil {
				r.Secret = string(s)
			}
		}
		rows = append(rows, r)
	}

	if *asJSON {
		return printJSON(out, map[string]any{"account": acct, "devices": rows})
	}
	if acct != nil {
		fmt.Fprintf(out, "Whisker account: %s (session in %s)\n\n", acct.Email, acct.Session)
	} else {
		fmt.Fprint(out, "Whisker account: not signed in\n\n")
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no saved robots")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLOCATION\tBACKEND\tSERIAL\tHOST\tPASSWORD\tDEFAULT")
	for _, r := range rows {
		def := ""
		if r.Default {
			def = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Location, r.Backend, dash(r.Serial), dash(r.Host), dash(r.Secret), def)
	}
	return tw.Flush()
}

func cmdConfigRemove(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config remove", flag.ContinueOnError)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Remove(name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %q\n", name)
	return nil
}

func cmdConfigDefault(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config default", flag.ContinueOnError)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Devices[name]; !ok {
		return fmt.Errorf("no robot named %q", name)
	}
	cfg.Default = name
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "default robot is now %q\n", name)
	return nil
}

// cmdConfigField shows or sets a device's location or host.
func cmdConfigField(args []string, out io.Writer, field string) error {
	if len(args) == 0 || len(args) > 2 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: litterrobot4-cli config %s <name> [<%s>]", field, field)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := args[0]
	dev, ok := cfg.Devices[name]
	if !ok {
		return fmt.Errorf("no robot named %q", name)
	}
	ptr := &dev.Location
	if field == "host" {
		ptr = &dev.Host
	}
	if len(args) == 1 {
		fmt.Fprintln(out, *ptr)
		return nil
	}

	value := strings.TrimSpace(args[1])
	switch field {
	case "location":
		if err := checkLocation(cfg, name, value); err != nil {
			return err
		}
	case "host":
		if value == "" && dev.Backend == config.BackendESPHome {
			return errors.New("ESPHome robots need a host")
		}
	}
	*ptr = value
	if err := cfg.Save(); err != nil {
		return err
	}
	if value == "" {
		fmt.Fprintf(out, "cleared the %s of %q\n", field, name)
	} else {
		fmt.Fprintf(out, "set the %s of %q to %q\n", field, name, value)
	}
	return nil
}

// checkLocation rejects a location that another robot already uses or
// that matches another robot's name, so --device stays unambiguous.
func checkLocation(cfg *config.Config, name, location string) error {
	if location == "" {
		return nil
	}
	for _, n := range cfg.Names() {
		if n == name {
			continue
		}
		if strings.EqualFold(n, location) || strings.EqualFold(cfg.Devices[n].Location, location) {
			return fmt.Errorf("location %q is already used by %q", location, n)
		}
	}
	return nil
}
