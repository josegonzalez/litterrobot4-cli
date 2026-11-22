package cloud

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
)

// DefaultConfirmTimeout is how long a Robot waits for a command to show
// up in the robot's reported state.
const DefaultConfirmTimeout = 30 * time.Second

// ErrNotConfirmed is returned when a command was accepted by the cloud
// but the robot's reported state did not change before the timeout.
var ErrNotConfirmed = errors.New("the command was sent, but the robot did not report the change in time")

// Robot is a cloud robot. It implements litterrobot.Robot.
//
// The cloud keeps the state the robot last reported, which does not
// change until the robot reports again. After a command, Robot sends
// requestState and polls until the change shows up, or until
// ConfirmTimeout passes.
type Robot struct {
	Client *Client
	Serial string
	// ConfirmTimeout bounds the wait after a command. Zero sends the
	// command without waiting.
	ConfirmTimeout time.Duration
	// PollInterval is the time between state checks. One second when zero.
	PollInterval time.Duration
}

var _ litterrobot.Robot = (*Robot)(nil)

// NewRobot returns a robot for the given serial.
func (c *Client) NewRobot(serial string) *Robot {
	return &Robot{Client: c, Serial: serial, ConfirmTimeout: DefaultConfirmTimeout}
}

// Data fetches the raw robot data.
func (r *Robot) Data(ctx context.Context) (RobotData, error) {
	return r.Client.RobotBySerial(ctx, r.Serial)
}

// State fetches the robot's state.
func (r *Robot) State(ctx context.Context) (*litterrobot.State, error) {
	d, err := r.Data(ctx)
	if err != nil {
		return nil, err
	}
	return d.State(), nil
}

// RequestState asks the robot to report its state to the cloud.
func (r *Robot) RequestState(ctx context.Context) error {
	return r.Client.SendCommand(ctx, r.Serial, CmdRequestState, nil)
}

// Send sends a command, then waits until done reports true for the
// robot's data. A nil done only asks the robot to report its state.
func (r *Robot) Send(ctx context.Context, command string, value any, done func(RobotData) bool) error {
	if err := r.Client.SendCommand(ctx, r.Serial, command, value); err != nil {
		return err
	}
	if r.ConfirmTimeout <= 0 {
		return nil
	}
	if done == nil {
		return r.RequestState(ctx)
	}

	interval := r.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(r.ConfirmTimeout)
	for {
		if err := r.RequestState(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		d, err := r.Data(ctx)
		if err != nil {
			return err
		}
		if done(d) {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrNotConfirmed
		}
	}
}

// Clean starts a clean cycle and waits for the robot to leave idle.
func (r *Robot) Clean(ctx context.Context) error {
	return r.Send(ctx, CmdCleanCycle, nil, func(d RobotData) bool {
		return d.str("robotStatus") != "ROBOT_IDLE"
	})
}

// Reset presses the reset button, which clears some faults.
func (r *Robot) Reset(ctx context.Context) error {
	return r.Send(ctx, CmdShortResetPress, nil, nil)
}

// SetPower turns the robot on or off.
func (r *Robot) SetPower(ctx context.Context, on bool) error {
	cmd := CmdPowerOff
	if on {
		cmd = CmdPowerOn
	}
	return r.Send(ctx, cmd, nil, func(d RobotData) bool {
		return (d.str("unitPowerStatus") == "ON") == on
	})
}

// SetPanelLock locks or unlocks the control panel.
func (r *Robot) SetPanelLock(ctx context.Context, on bool) error {
	cmd := CmdKeyPadLockOutOff
	if on {
		cmd = CmdKeyPadLockOutOn
	}
	return r.Send(ctx, cmd, nil, func(d RobotData) bool {
		return d.bool("isKeypadLockout") == on
	})
}

// SetNightLightMode sets the night light to off, on or auto.
func (r *Robot) SetNightLightMode(ctx context.Context, mode litterrobot.NightLightMode) error {
	cmds := map[litterrobot.NightLightMode]string{
		litterrobot.NightLightOff:  CmdNightLightModeOff,
		litterrobot.NightLightOn:   CmdNightLightModeOn,
		litterrobot.NightLightAuto: CmdNightLightModeAuto,
	}
	cmd, ok := cmds[mode]
	if !ok {
		return fmt.Errorf("unknown night light mode %q", mode)
	}
	return r.Send(ctx, cmd, nil, func(d RobotData) bool {
		return d.State().NightLightMode == mode
	})
}

// SetNightLightBrightness sets the night light brightness.
func (r *Robot) SetNightLightBrightness(ctx context.Context, level litterrobot.Brightness) error {
	p := level.Percent()
	if p == 0 {
		return fmt.Errorf("unknown brightness %q", level)
	}
	return r.Send(ctx, CmdSetNightLightValue, map[string]int{"nightLightPower": p}, func(d RobotData) bool {
		v, ok := d.num("nightLightBrightness")
		return ok && int(v) == p
	})
}

// SetPanelBrightness sets the control panel brightness.
func (r *Robot) SetPanelBrightness(ctx context.Context, level litterrobot.Brightness) error {
	cmds := map[litterrobot.Brightness]string{
		litterrobot.BrightnessLow:    CmdPanelBrightnessLow,
		litterrobot.BrightnessMedium: CmdPanelBrightnessMed,
		litterrobot.BrightnessHigh:   CmdPanelBrightnessHi,
	}
	cmd, ok := cmds[level]
	if !ok {
		return fmt.Errorf("unknown brightness %q", level)
	}
	return r.Send(ctx, cmd, nil, func(d RobotData) bool {
		return d.State().PanelBrightness == level
	})
}

// SetCleanCycleDelay sets the wait before a clean cycle, in minutes.
func (r *Robot) SetCleanCycleDelay(ctx context.Context, minutes int) error {
	if err := litterrobot.ValidateCleanCycleDelay(minutes); err != nil {
		return err
	}
	return r.Send(ctx, CmdSetClumpTime, map[string]int{"clumpTime": minutes}, func(d RobotData) bool {
		v, ok := d.num("cleanCycleWaitTime")
		return ok && int(v) == minutes
	})
}
