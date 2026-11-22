package cloud

import (
	"testing"

	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
)

func TestStatus(t *testing.T) {
	base := func(extra map[string]any) RobotData {
		d := RobotData{"isOnline": true, "unitPowerStatus": "ON", "robotStatus": "ROBOT_IDLE"}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	tests := []struct {
		name string
		data RobotData
		want litterrobot.Status
	}{
		{"offline wins", base(map[string]any{"isOnline": false, "displayCode": "DC_MTR_FAULT_PINCH"}), litterrobot.StatusOffline},
		{"missing online", RobotData{"unitPowerStatus": "ON"}, litterrobot.StatusOffline},
		{"power off", base(map[string]any{"unitPowerStatus": "OFF"}), litterrobot.StatusOff},
		{"fault display code", base(map[string]any{"displayCode": "DC_MTR_FAULT_PINCH", "robotCycleState": "CYCLE_STATE_PAUSE"}), litterrobot.StatusPinchDetect},
		{"over torque", base(map[string]any{"displayCode": "DC_MTR_FAULT_OT_SLP"}), litterrobot.StatusOverTorqueFault},
		{"cycle state beats robot status", base(map[string]any{"robotCycleState": "CYCLE_STATE_PAUSE", "robotStatus": "ROBOT_CLEAN"}), litterrobot.StatusPaused},
		{"cleaning", base(map[string]any{"robotStatus": "ROBOT_CLEAN"}), litterrobot.StatusCleanCycle},
		{"find dump is cleaning", base(map[string]any{"robotStatus": "ROBOT_FIND_DUMP"}), litterrobot.StatusCleanCycle},
		{"ready", base(nil), litterrobot.StatusReady},
		{"ready with cat", base(map[string]any{"displayCode": "DC_CAT_DETECT", "isDFIFull": true}), litterrobot.StatusCatDetected},
		{"ready with full drawer", base(map[string]any{"isDFIFull": true}), litterrobot.StatusDrawerFull},
		{"full drawer only matters when ready", base(map[string]any{"isDFIFull": true, "robotStatus": "ROBOT_CLEAN"}), litterrobot.StatusCleanCycle},
		{"unknown robot status", base(map[string]any{"robotStatus": "ROBOT_SOMETHING_NEW"}), litterrobot.StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.data.Status(); got != tt.want {
				t.Errorf("Status() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLitterLevel(t *testing.T) {
	if got, ok := (RobotData{"litterLevelPercentage": 0.42}).LitterLevel(); !ok || got != 42 {
		t.Errorf("percentage: got %v %v", got, ok)
	}
	// pylitterbot: max(round(100 - (level - 440) / 0.6, -1), 0)
	cases := map[float64]float64{440: 100, 470: 50, 500: 0, 600: 0, 452: 80}
	for level, want := range cases {
		if got, ok := (RobotData{"litterLevel": level}).LitterLevel(); !ok || got != want {
			t.Errorf("litterLevel %v: got %v, want %v", level, got, want)
		}
	}
	if _, ok := (RobotData{}).LitterLevel(); ok {
		t.Error("expected no level")
	}
}

func TestState(t *testing.T) {
	d := RobotData{
		"name": "Upstairs", "serial": "LR4C000001", "isOnline": true, "unitPowerStatus": "ON",
		"robotStatus": "ROBOT_IDLE", "unitPowerType": "AC", "DFILevelPercent": 37.0,
		"catWeight": 9.75, "nightLightMode": "AUTO", "nightLightBrightness": 50.0,
		"panelBrightnessHigh": 100.0, "isKeypadLockout": true, "cleanCycleWaitTime": 7.0,
		"sleepStatus": "WAKE", "odometerCleanCycles": 1234.0, "wifiRssi": -61.0,
		"espFirmware": "1.1.50", "picFirmwareVersion": "10512.2560.2.53",
		"lastSeen": "2026-11-22T15:04:05.000000Z", "isDFIFull": false,
	}
	s := d.State()
	if s.Name != "Upstairs" || s.Serial != "LR4C000001" || s.Status != litterrobot.StatusReady {
		t.Fatalf("basic fields: %+v", s)
	}
	if *s.WasteLevel != 37 || *s.CatWeight != 9.75 || *s.CleanCycleDelay != 7 || *s.CleanCycles != 1234 || *s.WiFiRSSI != -61 {
		t.Errorf("numbers: %+v", s)
	}
	if s.NightLightMode != litterrobot.NightLightAuto || s.NightLightBrightness != litterrobot.BrightnessMedium || s.PanelBrightness != litterrobot.BrightnessHigh {
		t.Errorf("lights: %+v", s)
	}
	if !*s.PanelLock || *s.Sleeping || !*s.Power || *s.DrawerFull {
		t.Errorf("flags: %+v", s)
	}
	if s.LastSeen == nil || s.LastSeen.Year() != 2026 {
		t.Errorf("last seen: %v", s.LastSeen)
	}
	if s.Firmware != "ESP 1.1.50, PIC 10512.2560.2.53" {
		t.Errorf("firmware: %q", s.Firmware)
	}
}
