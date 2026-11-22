// Command litterrobot4-cli discovers and controls Whisker Litter-Robot 4
// units, through the Whisker cloud for stock firmware or locally for
// robots running ESPHome firmware.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/josegonzalez/litterrobot4-cli/internal/config"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/cloud"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/esphome"
)

const usage = `Usage: litterrobot4-cli <command> [flags]

Commands:
  setup      Sign in, find robots and save the ones that are not set up yet
  login      Sign in to your Whisker account
  logout     Forget the Whisker account
  discover   Find robots on the local network and on your Whisker account
  status     Show a summary of every saved robot
  state      Show the full state of a robot
  clean      Start a clean cycle
  reset      Press the reset button
  power      Turn a robot on or off (power on|off)
  set        Change robot settings
  history    Show recent activity (cloud robots only)
  rename     Rename a robot in the Whisker app (cloud robots only)
  raw        Send a raw command
  config     Save, list and remove robots

Robot commands use a saved robot (--device, the default robot, or the only
saved robot), --serial for a robot on your Whisker account, or --host for a
robot running ESPHome firmware. The LITTERROBOT_DEVICE, LITTERROBOT_SERIAL
and LITTERROBOT_HOST environment variables also work.

Stock robots have no local API, so they are controlled through the Whisker
cloud. Secrets are kept in the OS keychain, or in the config file under
$XDG_CONFIG_HOME/litterrobot4-cli when no keychain is usable.

Run "litterrobot4-cli <command> -h" for command flags, or
"litterrobot4-cli version" for the version.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return flag.ErrHelp
	}

	switch args[0] {
	case "setup":
		return cmdSetup(ctx, args[1:], out)
	case "login":
		return cmdLogin(ctx, args[1:], out)
	case "logout":
		return cmdLogout(args[1:], out)
	case "discover":
		return cmdDiscover(ctx, args[1:], out)
	case "status":
		return cmdStatus(ctx, args[1:], out)
	case "state":
		return cmdState(ctx, args[1:], out)
	case "clean":
		return cmdAction(ctx, "clean", args[1:], out, func(ctx context.Context, r litterrobot.Robot) error { return r.Clean(ctx) })
	case "reset":
		return cmdAction(ctx, "reset", args[1:], out, func(ctx context.Context, r litterrobot.Robot) error { return r.Reset(ctx) })
	case "power":
		return cmdPower(ctx, args[1:], out)
	case "set":
		return cmdSet(ctx, args[1:], out)
	case "history":
		return cmdHistory(ctx, args[1:], out)
	case "rename":
		return cmdRename(ctx, args[1:], out)
	case "raw":
		return cmdRaw(ctx, args[1:], out)
	case "config":
		return cmdConfig(ctx, args[1:], out)
	case "version", "--version":
		return cmdVersion(out)
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func cmdDiscover(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "how long to search the local network")
	ifaceName := fs.String("interface", "", "network interface to search on (default: all)")
	noLAN := fs.Bool("no-lan", false, "skip the local network search")
	noCloud := fs.Bool("no-cloud", false, "skip listing robots on the Whisker account")
	asJSON := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var iface *net.Interface
	if *ifaceName != "" {
		var err error
		if iface, err = net.InterfaceByName(*ifaceName); err != nil {
			return err
		}
	}

	type result struct {
		Local []litterrobot.Device `json:"local"`
		Cloud []*litterrobot.State `json:"cloud"`
	}
	res := result{Local: []litterrobot.Device{}, Cloud: []*litterrobot.State{}}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if !*noLAN {
		devices, err := litterrobot.Discover(ctx, litterrobot.DiscoverOptions{Timeout: *timeout, Interface: iface})
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: local search failed:", err)
		}
		res.Local = append(res.Local, devices...)
	}
	cloudNote := ""
	if !*noCloud {
		client, err := cloudClient(cfg)
		switch {
		case errors.Is(err, errNoAccount):
			cloudNote = "not signed in to Whisker; run \"litterrobot4-cli login\" to list cloud robots"
		case err != nil:
			return err
		default:
			robots, err := client.Robots(ctx)
			if err != nil {
				return err
			}
			for _, r := range robots {
				res.Cloud = append(res.Cloud, r.State())
			}
		}
	}

	if *asJSON {
		return printJSON(out, res)
	}

	savedName := func(match func(*config.Device) bool) string {
		for _, n := range cfg.Names() {
			if match(cfg.Devices[n]) {
				return n
			}
		}
		return ""
	}

	if !*noLAN {
		fmt.Fprintln(out, "Local network:")
		if len(res.Local) == 0 {
			fmt.Fprintln(out, "  no robots found")
		} else {
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "  ADDRESS\tFIRMWARE\tHOSTNAME\tMAC\tSAVED AS")
			for _, d := range res.Local {
				fw := "stock"
				if d.Source == litterrobot.SourceESPHome {
					fw = "esphome"
				}
				saved := savedName(func(c *config.Device) bool {
					return c.Host == d.Address || c.Host == d.Hostname ||
						(d.MAC != "" && config.NormalizeMAC(c.MAC) == config.NormalizeMAC(d.MAC))
				})
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", d.Address, fw, d.Hostname, dash(d.MAC), dash(saved))
			}
			tw.Flush()
		}
		fmt.Fprintln(out, "  Stock robots are found by the hostname they register with your router.")
		fmt.Fprintln(out, "  Robots that did not register one do not show up here, but still work through the cloud.")
	}
	if !*noCloud {
		if !*noLAN {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "Whisker account:")
		switch {
		case cloudNote != "":
			fmt.Fprintln(out, "  "+cloudNote)
		case len(res.Cloud) == 0:
			fmt.Fprintln(out, "  no Litter-Robot 4 units on this account")
		default:
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "  SERIAL\tNAME\tSTATUS\tONLINE\tSAVED AS")
			for _, s := range res.Cloud {
				saved, _, _ := cfg.BySerial(s.Serial)
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", s.Serial, s.Name, s.Status, yesNo(s.Online), dash(saved))
			}
			tw.Flush()
		}
	}
	return nil
}

type deviceFlags struct {
	device   *string
	serial   *string
	host     *string
	username *string
	password *string
	wait     *time.Duration
	asJSON   *bool
}

func addDeviceFlags(fs *flag.FlagSet) *deviceFlags {
	return &deviceFlags{
		device:   fs.String("device", os.Getenv("LITTERROBOT_DEVICE"), "saved robot name, serial, host or location (env LITTERROBOT_DEVICE)"),
		serial:   fs.String("serial", os.Getenv("LITTERROBOT_SERIAL"), "serial of a robot on the Whisker account (env LITTERROBOT_SERIAL)"),
		host:     fs.String("host", os.Getenv("LITTERROBOT_HOST"), "address of a robot running ESPHome firmware (env LITTERROBOT_HOST)"),
		username: fs.String("username", envOr("LITTERROBOT_USERNAME", esphome.DefaultUsername), "ESPHome web server username, with --host (env LITTERROBOT_USERNAME)"),
		password: fs.String("password", os.Getenv("LITTERROBOT_PASSWORD"), "ESPHome web server password, with --host (env LITTERROBOT_PASSWORD)"),
		wait:     fs.Duration("wait", cloud.DefaultConfirmTimeout, "cloud robots: how long to wait for the robot to report a change (0 to not wait)"),
		asJSON:   fs.Bool("json", false, "print JSON output"),
	}
}

// target is a resolved robot.
type target struct {
	name    string
	device  *config.Device
	robot   litterrobot.Robot
	cloud   *cloud.Robot
	esphome *esphome.Client
}

// resolve picks the robot from the flags or the saved config.
func (f *deviceFlags) resolve() (*target, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	var (
		name string
		dev  *config.Device
		ok   bool
	)
	switch {
	case *f.device != "":
		if name, dev, ok = cfg.Lookup(*f.device); !ok {
			return nil, fmt.Errorf("no saved robot named %q (see \"config list\")", *f.device)
		}
	case *f.serial != "":
		if name, dev, ok = cfg.BySerial(*f.serial); !ok {
			dev = &config.Device{Backend: config.BackendCloud, Serial: *f.serial}
		}
	case *f.host != "":
		name, dev, ok = cfg.Lookup(*f.host)
		if !ok || dev.Backend != config.BackendESPHome {
			name, dev = "", &config.Device{Backend: config.BackendESPHome, Host: *f.host, Username: *f.username, Password: *f.password}
		}
	case cfg.Default != "":
		if name, dev, ok = cfg.Lookup(cfg.Default); !ok {
			return nil, fmt.Errorf("default robot %q is not saved", cfg.Default)
		}
	case len(cfg.Devices) == 1:
		name = cfg.Names()[0]
		dev = cfg.Devices[name]
	case len(cfg.Devices) == 0:
		return nil, errors.New("no robot given: run \"litterrobot4-cli setup\", or pass --serial or --host")
	default:
		return nil, errors.New("several robots are saved: pick one with --device, or set a default with \"config default\"")
	}
	t, err := newTarget(cfg, name, dev, *f.password)
	if err != nil {
		return nil, err
	}
	if t.cloud != nil {
		t.cloud.ConfirmTimeout = *f.wait
	}
	return t, nil
}

// unconfirmed turns cloud.ErrNotConfirmed into a warning, since the
// command itself was accepted.
func unconfirmed(err error, t *target) error {
	if errors.Is(err, cloud.ErrNotConfirmed) {
		fmt.Fprintf(os.Stderr, "warning: %s did not report the change within %s; it may still apply\n", t.label(), t.cloud.ConfirmTimeout)
		return nil
	}
	return err
}

func newTarget(cfg *config.Config, name string, dev *config.Device, password string) (*target, error) {
	t := &target{name: name, device: dev}
	switch dev.Backend {
	case config.BackendCloud:
		client, err := cloudClient(cfg)
		if err != nil {
			return nil, err
		}
		t.cloud = client.NewRobot(dev.Serial)
		t.robot = t.cloud
	case config.BackendESPHome:
		if password == "" && name != "" {
			p, _, err := cfg.Secret(config.DeviceKey(name, dev), dev.Password)
			if err != nil && !errors.Is(err, config.ErrNoSecret) {
				return nil, err
			}
			password = p
		}
		if password == "" {
			password = dev.Password
		}
		c, err := esphome.NewClient(dev.Host, dev.Username, password)
		if err != nil {
			return nil, err
		}
		t.esphome = c
		t.robot = c
	default:
		return nil, fmt.Errorf("robot %q has unknown backend %q", name, dev.Backend)
	}
	return t, nil
}

func (t *target) label() string {
	switch {
	case t.name != "":
		return t.name
	case t.device.Serial != "":
		return t.device.Serial
	}
	return t.device.Host
}

func cmdState(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	raw := fs.Bool("raw", false, "print the raw data from the API (cloud robots, with --json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	if *raw && t.cloud != nil {
		d, err := t.cloud.Data(ctx)
		if err != nil {
			return err
		}
		return printJSON(out, d)
	}
	s, err := t.robot.State(ctx)
	if err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, s)
	}
	printState(out, t, s)
	return nil
}

func cmdStatus(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Devices) == 0 {
		return errors.New("no saved robots: run \"litterrobot4-cli setup\"")
	}

	type row struct {
		Name     string             `json:"name"`
		Location string             `json:"location,omitempty"`
		State    *litterrobot.State `json:"state,omitempty"`
		Error    string             `json:"error,omitempty"`
	}
	rows := make([]row, 0, len(cfg.Devices))
	for _, n := range cfg.Names() {
		d := cfg.Devices[n]
		r := row{Name: n, Location: d.Location}
		t, err := newTarget(cfg, n, d, "")
		if err == nil {
			r.State, err = t.robot.State(ctx)
		}
		if err != nil {
			r.Error = err.Error()
		}
		rows = append(rows, r)
	}
	if *asJSON {
		return printJSON(out, rows)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLOCATION\tSTATUS\tLITTER\tWASTE\tCAT WEIGHT")
	for _, r := range rows {
		if r.State == nil {
			fmt.Fprintf(tw, "%s\t%s\terror: %s\t\t\t\n", r.Name, r.Location, r.Error)
			continue
		}
		s := r.State
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Location, s.Status, pct(s.LitterLevel), pct(s.WasteLevel), lbs(s.CatWeight))
	}
	return tw.Flush()
}

func cmdAction(ctx context.Context, name string, args []string, out io.Writer, fn func(context.Context, litterrobot.Robot) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	df := addDeviceFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	if err := unconfirmed(fn(ctx, t.robot), t); err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, map[string]any{"ok": true, "robot": t.label(), "command": name})
	}
	fmt.Fprintf(out, "sent %s to %s\n", name, t.label())
	return nil
}

func cmdPower(ctx context.Context, args []string, out io.Writer) error {
	var value string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		value, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("power", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if value == "" && fs.NArg() > 0 {
		value = fs.Arg(0)
	}
	on, err := parseOnOff(value)
	if err != nil {
		return errors.New("usage: litterrobot4-cli power on|off [flags]")
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	if err := unconfirmed(t.robot.SetPower(ctx, on), t); err != nil {
		return err
	}
	fmt.Fprintf(out, "turned %s %s\n", t.label(), onOff(on))
	return nil
}

func cmdSet(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	df := addDeviceFlags(fs)

	var steps []func(context.Context, litterrobot.Robot) error
	fs.Func("night-light", "night light mode (off|on|auto)", func(s string) error {
		m, err := litterrobot.ParseNightLightMode(s)
		if err != nil {
			return err
		}
		steps = append(steps, func(ctx context.Context, r litterrobot.Robot) error { return r.SetNightLightMode(ctx, m) })
		return nil
	})
	fs.Func("night-light-brightness", "night light brightness (low|medium|high)", func(s string) error {
		b, err := litterrobot.ParseBrightness(s)
		if err != nil {
			return err
		}
		steps = append(steps, func(ctx context.Context, r litterrobot.Robot) error { return r.SetNightLightBrightness(ctx, b) })
		return nil
	})
	fs.Func("panel-brightness", "control panel brightness (low|medium|high)", func(s string) error {
		b, err := litterrobot.ParseBrightness(s)
		if err != nil {
			return err
		}
		steps = append(steps, func(ctx context.Context, r litterrobot.Robot) error { return r.SetPanelBrightness(ctx, b) })
		return nil
	})
	fs.Func("panel-lock", "lock the control panel (on|off)", func(s string) error {
		on, err := parseOnOff(s)
		if err != nil {
			return err
		}
		steps = append(steps, func(ctx context.Context, r litterrobot.Robot) error { return r.SetPanelLock(ctx, on) })
		return nil
	})
	fs.Func("clean-delay", "minutes to wait after a cat leaves before cleaning (3|7|15|25|30)", func(s string) error {
		m, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		if err := litterrobot.ValidateCleanCycleDelay(m); err != nil {
			return err
		}
		steps = append(steps, func(ctx context.Context, r litterrobot.Robot) error { return r.SetCleanCycleDelay(ctx, m) })
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(steps) == 0 {
		fs.Usage()
		return errors.New("nothing to set")
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	for _, step := range steps {
		if err := unconfirmed(step(ctx, t.robot), t); err != nil {
			return err
		}
	}
	if *df.asJSON {
		return printJSON(out, map[string]any{"ok": true, "robot": t.label()})
	}
	fmt.Fprintf(out, "updated %s\n", t.label())
	return nil
}

func cmdHistory(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	since := fs.Duration("since", 24*time.Hour, "how far back to look")
	limit := fs.Int("limit", 50, "maximum number of entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	if t.cloud == nil {
		return errors.New("history is only available for robots on the Whisker cloud")
	}
	now := time.Now()
	entries, err := t.cloud.Client.Activity(ctx, t.cloud.Serial, now.Add(-*since), now, *limit)
	if err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "no activity")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tEVENT\tVALUE")
	for _, e := range entries {
		ts := e.Timestamp
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = parsed.Local().Format("2006-01-02 15:04:05")
		}
		value := e.StateString
		if value == "" {
			value = e.ValueString
		}
		if value == "" && e.Value != nil {
			value = fmt.Sprint(e.Value)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", ts, e.Measure, value)
	}
	return tw.Flush()
}

func cmdRename(ctx context.Context, args []string, out io.Writer) error {
	var newName string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		newName, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if newName == "" && fs.NArg() > 0 {
		newName = fs.Arg(0)
	}
	if strings.TrimSpace(newName) == "" {
		return errors.New("usage: litterrobot4-cli rename <new name> [flags]")
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}
	if t.cloud == nil {
		return errors.New("rename is only available for robots on the Whisker cloud")
	}
	if err := t.cloud.Client.Rename(ctx, t.cloud.Serial, newName); err != nil {
		return err
	}
	fmt.Fprintf(out, "renamed %s to %q in the Whisker app\n", t.label(), newName)
	return nil
}

func cmdRaw(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("raw", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	command := fs.String("command", "", "cloud: command name, such as requestState or cleanCycle")
	value := fs.String("value", "", "cloud: JSON value sent with the command, such as '{\"clumpTime\":7}'")
	path := fs.String("path", "", "esphome: REST path, such as /sensor/Litter Level or /button/Manual Cycle/press")
	method := fs.String("method", "GET", "esphome: HTTP method")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := df.resolve()
	if err != nil {
		return err
	}

	if t.cloud != nil {
		if *command == "" {
			return errors.New("--command is required for cloud robots")
		}
		var v any
		if *value != "" {
			if err := json.Unmarshal([]byte(*value), &v); err != nil {
				return fmt.Errorf("--value: %w", err)
			}
		}
		if err := t.cloud.Client.SendCommand(ctx, t.cloud.Serial, *command, v); err != nil {
			return err
		}
		fmt.Fprintf(out, "sent %s to %s\n", *command, t.label())
		return nil
	}

	if *path == "" {
		return errors.New("--path is required for ESPHome robots")
	}
	domain, rest, _ := strings.Cut(strings.TrimPrefix(*path, "/"), "/")
	name, action, _ := strings.Cut(rest, "/")
	if strings.EqualFold(*method, "GET") {
		e, err := t.esphome.Get(ctx, domain, name)
		if err != nil {
			return err
		}
		return printJSON(out, e)
	}
	if err := t.esphome.Post(ctx, domain, name, action, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s ok\n", strings.ToUpper(*method), *path)
	return nil
}

func printState(out io.Writer, t *target, s *litterrobot.State) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	row := func(k string, v any) { fmt.Fprintf(tw, "%s:\t%v\n", k, v) }
	flag := func(k string, v *bool) {
		if v != nil {
			row(k, onOff(*v))
		}
	}

	if s.Name != "" {
		row("Name", s.Name)
	}
	if s.Serial != "" {
		row("Serial", s.Serial)
	}
	if t.device.Location != "" {
		row("Location", t.device.Location)
	}
	if t.esphome != nil {
		row("Host", t.device.Host)
	}
	row("Status", s.Status)
	if t.cloud != nil {
		row("Online", yesNo(s.Online))
	}
	flag("Power", s.Power)
	if s.PowerType != "" {
		row("Power source", powerType(s.PowerType))
	}
	if s.LitterLevel != nil {
		level := pct(s.LitterLevel)
		if s.LitterLevelState != "" {
			level += " (" + strings.ToLower(s.LitterLevelState) + ")"
		}
		row("Litter level", level)
	}
	if s.WasteLevel != nil {
		row("Waste drawer", pct(s.WasteLevel))
	}
	if s.DrawerFull != nil {
		row("Drawer full", yesNo(*s.DrawerFull))
	}
	if s.CatWeight != nil {
		row("Last cat weight", lbs(s.CatWeight))
	}
	if s.CleanCycleDelay != nil {
		row("Clean delay", fmt.Sprintf("%d min", *s.CleanCycleDelay))
	}
	if s.NightLightMode != "" {
		nl := string(s.NightLightMode)
		if s.NightLightBrightness != "" {
			nl += " (" + string(s.NightLightBrightness) + ")"
		}
		row("Night light", nl)
	}
	if s.PanelBrightness != "" {
		row("Panel brightness", s.PanelBrightness)
	}
	flag("Panel lock", s.PanelLock)
	if s.Sleeping != nil {
		row("Sleeping", yesNo(*s.Sleeping))
	}
	if s.CleanCycles != nil {
		row("Clean cycles", *s.CleanCycles)
	}
	if s.Fault != "" {
		row("Fault", s.Fault)
	}
	if s.Firmware != "" {
		row("Firmware", s.Firmware)
	}
	if s.WiFiRSSI != nil {
		row("Wi-Fi RSSI", fmt.Sprintf("%d dBm", *s.WiFiRSSI))
	}
	if s.LastSeen != nil {
		row("Last seen", s.LastSeen.Local().Format(time.RFC1123))
	}
	tw.Flush()
}

func powerType(s string) string {
	switch s {
	case "AC":
		return "mains (AC)"
	case "DC":
		return "battery backup (DC)"
	case "NC":
		return "not connected"
	}
	return s
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", s)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pct(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64) + "%"
}

func lbs(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64) + " lb"
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
