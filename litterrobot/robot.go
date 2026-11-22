// Package litterrobot holds the types shared by the Litter-Robot 4
// backends: the Whisker cloud API (package cloud) for stock firmware and
// the ESPHome web server (package esphome) for robots flashed with
// ESPHome firmware.
package litterrobot

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Robot is a Litter-Robot 4 that can be read and controlled.
type Robot interface {
	State(ctx context.Context) (*State, error)
	Clean(ctx context.Context) error
	Reset(ctx context.Context) error
	SetPower(ctx context.Context, on bool) error
	SetPanelLock(ctx context.Context, on bool) error
	SetNightLightMode(ctx context.Context, mode NightLightMode) error
	SetNightLightBrightness(ctx context.Context, level Brightness) error
	SetPanelBrightness(ctx context.Context, level Brightness) error
	SetCleanCycleDelay(ctx context.Context, minutes int) error
}

// State is a snapshot of a robot. Pointer fields are nil when the
// backend does not report them.
type State struct {
	Name   string `json:"name,omitempty"`
	Serial string `json:"serial,omitempty"`
	Status Status `json:"status"`
	Online bool   `json:"online"`
	Power  *bool  `json:"power,omitempty"`
	// PowerType is AC (mains), DC (battery backup) or NC (not connected).
	PowerType string `json:"powerType,omitempty"`
	// LitterLevel and WasteLevel are percentages.
	LitterLevel *float64 `json:"litterLevel,omitempty"`
	// LitterLevelState is the app's litter rating: OVERFILL, OPTIMAL,
	// REFILL, LOW or EMPTY.
	LitterLevelState string   `json:"litterLevelState,omitempty"`
	WasteLevel       *float64 `json:"wasteLevel,omitempty"`
	DrawerFull       *bool    `json:"drawerFull,omitempty"`
	// CatWeight is in pounds.
	CatWeight            *float64       `json:"catWeight,omitempty"`
	NightLightMode       NightLightMode `json:"nightLightMode,omitempty"`
	NightLightBrightness Brightness     `json:"nightLightBrightness,omitempty"`
	PanelBrightness      Brightness     `json:"panelBrightness,omitempty"`
	PanelLock            *bool          `json:"panelLock,omitempty"`
	// CleanCycleDelay is the wait after a cat leaves, in minutes.
	CleanCycleDelay *int       `json:"cleanCycleDelay,omitempty"`
	Sleeping        *bool      `json:"sleeping,omitempty"`
	CleanCycles     *int       `json:"cleanCycles,omitempty"`
	Firmware        string     `json:"firmware,omitempty"`
	WiFiRSSI        *int       `json:"wifiRssi,omitempty"`
	LastSeen        *time.Time `json:"lastSeen,omitempty"`
	Fault           string     `json:"fault,omitempty"`
}

// Status is the robot's status.
type Status struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

func (s Status) String() string { return s.Text }

// Known statuses, as reported by the Whisker app.
var (
	StatusBonnetRemoved      = Status{"BR", "Bonnet Removed"}
	StatusCleanCycleComplete = Status{"CCC", "Clean Cycle Complete"}
	StatusCleanCycle         = Status{"CCP", "Clean Cycle In Progress"}
	StatusCatDetected        = Status{"CD", "Cat Detected"}
	StatusCatSensorFault     = Status{"CSF", "Cat Sensor Fault"}
	StatusCatSensorInterrupt = Status{"CSI", "Cat Sensor Interrupted"}
	StatusCatSensorTiming    = Status{"CST", "Cat Sensor Timing"}
	StatusDrawerFull1        = Status{"DF1", "Drawer Almost Full - 2 Cycles Left"}
	StatusDrawerFull2        = Status{"DF2", "Drawer Almost Full - 1 Cycle Left"}
	StatusDrawerFull         = Status{"DFS", "Drawer Full"}
	StatusDumpHomeFault      = Status{"DHF", "Dump + Home Position Fault"}
	StatusDumpPositionFault  = Status{"DPF", "Dump Position Fault"}
	StatusEmptyCycle         = Status{"EC", "Empty Cycle"}
	StatusHomePositionFault  = Status{"HPF", "Home Position Fault"}
	StatusOff                = Status{"OFF", "Off"}
	StatusOffline            = Status{"OFFLINE", "Offline"}
	StatusOverTorqueFault    = Status{"OTF", "Over Torque Fault"}
	StatusPaused             = Status{"P", "Clean Cycle Paused"}
	StatusPinchDetect        = Status{"PD", "Pinch Detect"}
	StatusPowerDown          = Status{"PWRD", "Powering Down"}
	StatusPowerUp            = Status{"PWRU", "Powering Up"}
	StatusReady              = Status{"RDY", "Ready"}
	StatusStartupCatSensor   = Status{"SCF", "Cat Sensor Fault At Startup"}
	StatusStartupDrawerFull  = Status{"SDF", "Drawer Full At Startup"}
	StatusStartupPinchDetect = Status{"SPF", "Pinch Detect At Startup"}
	StatusUnknown            = Status{"", "Unknown"}
)

// NightLightMode is the night light setting.
type NightLightMode string

// Night light modes.
const (
	NightLightOff  NightLightMode = "off"
	NightLightOn   NightLightMode = "on"
	NightLightAuto NightLightMode = "auto"
)

// ParseNightLightMode parses off, on or auto.
func ParseNightLightMode(s string) (NightLightMode, error) {
	switch m := NightLightMode(strings.ToLower(strings.TrimSpace(s))); m {
	case NightLightOff, NightLightOn, NightLightAuto:
		return m, nil
	}
	return "", fmt.Errorf("night light mode must be off, on or auto, not %q", s)
}

// Brightness is a night light or panel brightness level.
type Brightness string

// Brightness levels.
const (
	BrightnessLow    Brightness = "low"
	BrightnessMedium Brightness = "medium"
	BrightnessHigh   Brightness = "high"
)

// ParseBrightness parses low, medium (or med) or high.
func ParseBrightness(s string) (Brightness, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return BrightnessLow, nil
	case "medium", "med":
		return BrightnessMedium, nil
	case "high":
		return BrightnessHigh, nil
	}
	return "", fmt.Errorf("brightness must be low, medium or high, not %q", s)
}

// Percent is the brightness as the Whisker API's 25/50/100 value.
func (b Brightness) Percent() int {
	switch b {
	case BrightnessLow:
		return 25
	case BrightnessMedium:
		return 50
	case BrightnessHigh:
		return 100
	}
	return 0
}

// BrightnessFromPercent maps 25/50/100 to a level, or "" for other values.
func BrightnessFromPercent(p int) Brightness {
	switch p {
	case 25:
		return BrightnessLow
	case 50:
		return BrightnessMedium
	case 100:
		return BrightnessHigh
	}
	return ""
}

// CleanCycleDelays are the wait times, in minutes, the robot accepts.
var CleanCycleDelays = []int{3, 7, 15, 25, 30}

// ValidateCleanCycleDelay checks minutes against CleanCycleDelays.
func ValidateCleanCycleDelay(minutes int) error {
	for _, d := range CleanCycleDelays {
		if d == minutes {
			return nil
		}
	}
	return fmt.Errorf("clean cycle delay must be one of 3, 7, 15, 25 or 30 minutes, not %d", minutes)
}
