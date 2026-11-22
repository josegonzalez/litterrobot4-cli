package cloud

import (
	"math"
	"strings"
	"time"

	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
)

var robotStatusMap = map[string]litterrobot.Status{
	"ROBOT_BONNET":           litterrobot.StatusBonnetRemoved,
	"ROBOT_CAT_DETECT":       litterrobot.StatusCatDetected,
	"ROBOT_CAT_DETECT_DELAY": litterrobot.StatusCatSensorTiming,
	"ROBOT_CLEAN":            litterrobot.StatusCleanCycle,
	"ROBOT_EMPTY":            litterrobot.StatusEmptyCycle,
	"ROBOT_FIND_DUMP":        litterrobot.StatusCleanCycle,
	"ROBOT_IDLE":             litterrobot.StatusReady,
	"ROBOT_POWER_DOWN":       litterrobot.StatusPowerDown,
	"ROBOT_POWER_OFF":        litterrobot.StatusOff,
	"ROBOT_POWER_UP":         litterrobot.StatusPowerUp,
}

var cycleStateMap = map[string]litterrobot.Status{
	"CYCLE_STATE_CAT_DETECT": litterrobot.StatusCatSensorInterrupt,
	"CYCLE_STATE_PAUSE":      litterrobot.StatusPaused,
}

var displayFaultMap = map[string]litterrobot.Status{
	"DC_CAT_DETECT_30M":   litterrobot.StatusCatSensorFault,
	"DC_MTR_FAULT_OT_AMP": litterrobot.StatusOverTorqueFault,
	"DC_MTR_FAULT_OT_SLP": litterrobot.StatusOverTorqueFault,
	"DC_MTR_FAULT_PINCH":  litterrobot.StatusPinchDetect,
}

// Status works out the robot's status the same way the Whisker app and
// pylitterbot do.
func (d RobotData) Status() litterrobot.Status {
	if !d.bool("isOnline") {
		return litterrobot.StatusOffline
	}
	if d.str("unitPowerStatus") != "ON" {
		return litterrobot.StatusOff
	}
	display := d.str("displayCode")
	if s, ok := displayFaultMap[display]; ok {
		return s
	}
	if s, ok := cycleStateMap[d.str("robotCycleState")]; ok {
		return s
	}
	s, ok := robotStatusMap[d.str("robotStatus")]
	if !ok {
		return litterrobot.StatusUnknown
	}
	if s == litterrobot.StatusReady {
		if display == "DC_CAT_DETECT" {
			return litterrobot.StatusCatDetected
		}
		if d.bool("isDFIFull") {
			return litterrobot.StatusDrawerFull
		}
	}
	return s
}

// LitterLevel returns the litter level as a percentage.
func (d RobotData) LitterLevel() (float64, bool) {
	if p, ok := d.num("litterLevelPercentage"); ok {
		return p * 100, true
	}
	level, ok := d.num("litterLevel")
	if !ok {
		return 0, false
	}
	// pylitterbot: max(round(100 - (level - 440) / 0.6, -1), 0)
	p := math.Round((100-(level-440)/0.6)/10) * 10
	return math.Max(p, 0), true
}

// State converts the raw data to a litterrobot.State.
func (d RobotData) State() *litterrobot.State {
	s := &litterrobot.State{
		Name:      d.Name(),
		Serial:    d.Serial(),
		Status:    d.Status(),
		Online:    d.bool("isOnline"),
		PowerType: d.str("unitPowerType"),

		LitterLevelState: d.str("litterLevelState"),
		Firmware:         d.firmware(),
	}
	power := d.str("unitPowerStatus") == "ON"
	s.Power = &power
	if v, ok := d.LitterLevel(); ok {
		s.LitterLevel = &v
	}
	if v, ok := d.num("DFILevelPercent"); ok {
		s.WasteLevel = &v
	}
	if v, ok := d["isDFIFull"].(bool); ok {
		s.DrawerFull = &v
	}
	if v, ok := d.num("catWeight"); ok {
		s.CatWeight = &v
	}
	if m := strings.ToLower(d.str("nightLightMode")); m != "" {
		s.NightLightMode = litterrobot.NightLightMode(m)
	}
	if v, ok := d.num("nightLightBrightness"); ok {
		s.NightLightBrightness = litterrobot.BrightnessFromPercent(int(v))
	}
	if v, ok := d.num("panelBrightnessHigh"); ok {
		s.PanelBrightness = litterrobot.BrightnessFromPercent(int(v))
	}
	if v, ok := d["isKeypadLockout"].(bool); ok {
		s.PanelLock = &v
	}
	if v, ok := d.num("cleanCycleWaitTime"); ok {
		i := int(v)
		s.CleanCycleDelay = &i
	}
	if v := d.str("sleepStatus"); v != "" {
		sleeping := v != "WAKE"
		s.Sleeping = &sleeping
	}
	if v, ok := d.num("odometerCleanCycles"); ok {
		i := int(v)
		s.CleanCycles = &i
	}
	if v, ok := d.num("wifiRssi"); ok {
		i := int(v)
		s.WiFiRSSI = &i
	}
	if v := d.str("lastSeen"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			s.LastSeen = &t
		}
	}
	if v := d.str("displayCode"); displayFaultMap[v] != (litterrobot.Status{}) {
		s.Fault = v
	}
	return s
}

func (d RobotData) firmware() string {
	var parts []string
	if v := d.str("espFirmware"); v != "" {
		parts = append(parts, "ESP "+v)
	}
	if v := d.str("picFirmwareVersion"); v != "" {
		parts = append(parts, "PIC "+v)
	}
	if v := d.str("laserBoardFirmwareVersion"); v != "" {
		parts = append(parts, "laser "+v)
	}
	return strings.Join(parts, ", ")
}

func (d RobotData) str(key string) string {
	s, _ := d[key].(string)
	return s
}

func (d RobotData) bool(key string) bool {
	b, _ := d[key].(bool)
	return b
}

func (d RobotData) num(key string) (float64, bool) {
	switch v := d[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}
