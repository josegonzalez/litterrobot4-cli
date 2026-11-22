// Package esphome controls Litter-Robot 4 units flashed with the
// open source ESPHome firmware (codeberg.org/Joseph-DiGiovanni/esphome-litter-robot)
// through the ESPHome web server REST API.
package esphome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
)

// DefaultUsername is the web server user in the firmware's example config.
const DefaultUsername = "user"

// Entity names used by the firmware's litter-robot4 package.
const (
	ButtonCycle            = "Manual Cycle"
	ButtonReset            = "Reset"
	SwitchPower            = "Power"
	SwitchPanelLock        = "Control Panel Lockout"
	SelectNightLightMode   = "Night Light Mode"
	SelectNightLightBright = "Night Light Brightness"
	SelectPanelBrightness  = "Panel Brightness"
	NumberCleanCycleDelay  = "Clean Cycle Delay"
	SensorWasteLevel       = "Waste Drawer Level"
	SensorLitterLevel      = "Litter Level"
	SensorCatWeight        = "Last Cat Weight"
	SensorCleanCycles      = "Clean Cycles"
	BinaryDrawerFull       = "Waste Drawer Full"
	BinarySleeping         = "Sleeping"
	BinaryWeightDetected   = "Weight Detected"
	BinaryCurtainDetected  = "Curtain Detected"
	TextFault              = "Fault"
	TextPowerType          = "Power Type"
	TextRobotStatus        = "Robot Status"
)

// ErrNotFound is returned when the device has no such entity.
var ErrNotFound = errors.New("entity not found")

// Entity is an entity state from the web server.
type Entity struct {
	ID     string          `json:"id"`
	Name   string          `json:"name,omitempty"`
	State  string          `json:"state"`
	Value  json.RawMessage `json:"value,omitempty"`
	Option []string        `json:"option,omitempty"`
}

// Float returns a numeric value.
func (e *Entity) Float() (float64, bool) {
	var f float64
	if json.Unmarshal(e.Value, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(e.Value, &s) == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	if f, err := strconv.ParseFloat(strings.Fields(e.State + " x")[0], 64); err == nil {
		return f, true
	}
	return 0, false
}

// Bool returns a boolean value.
func (e *Entity) Bool() (bool, bool) {
	var b bool
	if json.Unmarshal(e.Value, &b) == nil {
		return b, true
	}
	switch strings.ToUpper(e.State) {
	case "ON":
		return true, true
	case "OFF":
		return false, true
	}
	return false, false
}

// Text returns a string value.
func (e *Entity) Text() string {
	var s string
	if json.Unmarshal(e.Value, &s) == nil {
		return s
	}
	return e.State
}

// Client talks to one device's web server. It implements
// litterrobot.Robot.
type Client struct {
	// Base is the device URL, such as http://192.168.1.50.
	Base *url.URL
	HTTP *http.Client
}

var _ litterrobot.Robot = (*Client)(nil)

// NewClient returns a client for host (an IP address, hostname or URL).
// Empty credentials disable authentication.
func NewClient(host, username, password string) (*Client, error) {
	if host == "" {
		return nil, errors.New("host is required")
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return &Client{
		Base: u,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &digestTransport{
				base:     http.DefaultTransport,
				username: username,
				password: password,
			},
		},
	}, nil
}

// ObjectID returns the ESPHome object id for an entity name.
func ObjectID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		}
	}
	return b.String()
}

// do sends a request for an entity, trying its name and then its object
// id, since the URL format changed between ESPHome versions.
func (c *Client) do(ctx context.Context, method, domain, name, action string, query url.Values) ([]byte, error) {
	var lastErr error
	for _, id := range []string{name, ObjectID(name)} {
		u := *c.Base
		u.Path += "/" + domain + "/" + id
		u.RawPath = c.Base.Path + "/" + domain + "/" + url.PathEscape(id)
		if action != "" {
			u.Path += "/" + action
			u.RawPath += "/" + action
		}
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			lastErr = fmt.Errorf("%s %q: %w", domain, name, ErrNotFound)
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, errors.New("esphome: the device rejected the web server username or password")
		case resp.StatusCode >= 300:
			return nil, fmt.Errorf("esphome: %s %s: %s", method, u.Path, resp.Status)
		}
		return body, nil
	}
	return nil, lastErr
}

// Get reads an entity.
func (c *Client) Get(ctx context.Context, domain, name string) (*Entity, error) {
	body, err := c.do(ctx, http.MethodGet, domain, name, "", url.Values{"detail": {"all"}})
	if err != nil {
		return nil, err
	}
	var e Entity
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("esphome: %s %q: %w", domain, name, err)
	}
	return &e, nil
}

// Post invokes an action on an entity, such as press, turn_on or set.
func (c *Client) Post(ctx context.Context, domain, name, action string, query url.Values) error {
	_, err := c.do(ctx, http.MethodPost, domain, name, action, query)
	return err
}

// Ping checks that the device answers and accepts the credentials.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Get(ctx, "switch", SwitchPower)
	return err
}

// State reads the robot's entities. Entities the firmware does not
// expose are left unset.
func (c *Client) State(ctx context.Context) (*litterrobot.State, error) {
	s := &litterrobot.State{Online: true}
	get := func(domain, name string) *Entity {
		e, err := c.Get(ctx, domain, name)
		if err != nil {
			return nil
		}
		return e
	}

	power := get("switch", SwitchPower)
	if power == nil {
		// Fail loudly when the device itself is unreachable.
		if err := c.Ping(ctx); err != nil {
			return nil, err
		}
	} else if v, ok := power.Bool(); ok {
		s.Power = &v
	}
	num := func(domain, name string) *float64 {
		if e := get(domain, name); e != nil {
			if v, ok := e.Float(); ok {
				return &v
			}
		}
		return nil
	}
	flag := func(domain, name string) *bool {
		if e := get(domain, name); e != nil {
			if v, ok := e.Bool(); ok {
				return &v
			}
		}
		return nil
	}
	text := func(domain, name string) string {
		if e := get(domain, name); e != nil {
			return e.Text()
		}
		return ""
	}

	s.WasteLevel = num("sensor", SensorWasteLevel)
	s.LitterLevel = num("sensor", SensorLitterLevel)
	s.CatWeight = num("sensor", SensorCatWeight)
	if v := num("sensor", SensorCleanCycles); v != nil {
		i := int(*v)
		s.CleanCycles = &i
	}
	if v := num("number", NumberCleanCycleDelay); v != nil {
		i := int(*v)
		s.CleanCycleDelay = &i
	}
	s.DrawerFull = flag("binary_sensor", BinaryDrawerFull)
	s.Sleeping = flag("binary_sensor", BinarySleeping)
	s.PanelLock = flag("switch", SwitchPanelLock)
	s.PowerType = text("text_sensor", TextPowerType)
	s.Fault = text("text_sensor", TextFault)
	if m, err := litterrobot.ParseNightLightMode(text("select", SelectNightLightMode)); err == nil {
		s.NightLightMode = m
	}
	if b, err := litterrobot.ParseBrightness(text("select", SelectNightLightBright)); err == nil {
		s.NightLightBrightness = b
	}
	if b, err := litterrobot.ParseBrightness(text("select", SelectPanelBrightness)); err == nil {
		s.PanelBrightness = b
	}

	s.Status = c.status(s, text("text_sensor", TextRobotStatus), flag("binary_sensor", BinaryWeightDetected))
	return s, nil
}

// status approximates the Whisker status from the firmware's entities.
func (c *Client) status(s *litterrobot.State, robotStatus string, weight *bool) litterrobot.Status {
	if s.Power != nil && !*s.Power {
		return litterrobot.StatusOff
	}
	if f := strings.TrimSpace(s.Fault); f != "" && !strings.EqualFold(f, "none") && !strings.EqualFold(f, "ok") {
		return litterrobot.Status{Code: "FAULT", Text: f}
	}
	if robotStatus != "" {
		return litterrobot.Status{Text: robotStatus}
	}
	if s.DrawerFull != nil && *s.DrawerFull {
		return litterrobot.StatusDrawerFull
	}
	if weight != nil && *weight {
		return litterrobot.StatusCatDetected
	}
	return litterrobot.StatusReady
}

// Clean starts a clean cycle.
func (c *Client) Clean(ctx context.Context) error {
	return c.Post(ctx, "button", ButtonCycle, "press", nil)
}

// Reset presses the reset button.
func (c *Client) Reset(ctx context.Context) error {
	return c.Post(ctx, "button", ButtonReset, "press", nil)
}

func (c *Client) setSwitch(ctx context.Context, name string, on bool) error {
	action := "turn_off"
	if on {
		action = "turn_on"
	}
	return c.Post(ctx, "switch", name, action, nil)
}

// SetPower turns the robot on or off.
func (c *Client) SetPower(ctx context.Context, on bool) error {
	return c.setSwitch(ctx, SwitchPower, on)
}

// SetPanelLock locks or unlocks the control panel.
func (c *Client) SetPanelLock(ctx context.Context, on bool) error {
	return c.setSwitch(ctx, SwitchPanelLock, on)
}

// setSelect picks the select option that matches want, ignoring case,
// using the options the device reports.
func (c *Client) setSelect(ctx context.Context, name string, want ...string) error {
	e, err := c.Get(ctx, "select", name)
	if err != nil {
		return err
	}
	option := ""
	for _, o := range e.Option {
		for _, w := range want {
			if strings.EqualFold(o, w) {
				option = o
				break
			}
		}
		if option != "" {
			break
		}
	}
	if option == "" {
		if len(e.Option) > 0 {
			return fmt.Errorf("esphome: %q has no option %q (options: %s)", name, want[0], strings.Join(e.Option, ", "))
		}
		option = strings.ToUpper(want[0][:1]) + want[0][1:]
	}
	return c.Post(ctx, "select", name, "set", url.Values{"option": {option}})
}

// SetNightLightMode sets the night light to off, on or auto.
func (c *Client) SetNightLightMode(ctx context.Context, mode litterrobot.NightLightMode) error {
	return c.setSelect(ctx, SelectNightLightMode, string(mode))
}

// SetNightLightBrightness sets the night light brightness.
func (c *Client) SetNightLightBrightness(ctx context.Context, level litterrobot.Brightness) error {
	return c.setSelect(ctx, SelectNightLightBright, brightnessOptions(level)...)
}

// SetPanelBrightness sets the control panel brightness.
func (c *Client) SetPanelBrightness(ctx context.Context, level litterrobot.Brightness) error {
	return c.setSelect(ctx, SelectPanelBrightness, brightnessOptions(level)...)
}

func brightnessOptions(level litterrobot.Brightness) []string {
	if level == litterrobot.BrightnessMedium {
		return []string{"medium", "med"}
	}
	return []string{string(level)}
}

// SetCleanCycleDelay sets the wait before a clean cycle, in minutes.
func (c *Client) SetCleanCycleDelay(ctx context.Context, minutes int) error {
	if err := litterrobot.ValidateCleanCycleDelay(minutes); err != nil {
		return err
	}
	return c.Post(ctx, "number", NumberCleanCycleDelay, "set", url.Values{"value": {strconv.Itoa(minutes)}})
}
