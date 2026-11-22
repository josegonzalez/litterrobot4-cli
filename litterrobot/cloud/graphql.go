package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Endpoint is the Litter-Robot 4 GraphQL endpoint.
const Endpoint = "https://lr4.iothings.site/graphql"

// CommandSource is sent with commands so they show up as coming from
// this tool in the robot's activity.
const CommandSource = "litterrobot4-cli"

// robotFields is the field selection for a Litter-Robot 4.
const robotFields = `{
  unitId name serial userId espFirmware picFirmwareVersion picFirmwareVersionHex
  laserBoardFirmwareVersion laserBoardFirmwareVersionHex wifiRssi unitPowerType
  catWeight displayCode unitTimezone unitTime cleanCycleWaitTime isKeypadLockout
  nightLightMode nightLightBrightness isPanelSleepMode panelSleepTime panelWakeTime
  weekdaySleepModeEnabled {
    Sunday { sleepTime wakeTime isEnabled }
    Monday { sleepTime wakeTime isEnabled }
    Tuesday { sleepTime wakeTime isEnabled }
    Wednesday { sleepTime wakeTime isEnabled }
    Thursday { sleepTime wakeTime isEnabled }
    Friday { sleepTime wakeTime isEnabled }
    Saturday { sleepTime wakeTime isEnabled }
  }
  unitPowerStatus sleepStatus robotStatus globeMotorFaultStatus pinchStatus catDetect
  isBonnetRemoved isNightLightLEDOn odometerPowerCycles odometerCleanCycles
  panelBrightnessHigh panelBrightnessLow smartWeightEnabled odometerEmptyCycles
  odometerFilterCycles isDFIResetPending DFINumberOfCycles DFILevelPercent isDFIFull
  DFIFullCounter DFITriggerCount litterLevel DFILevelMM isCatDetectPending
  globeMotorRetractFaultStatus robotCycleStatus robotCycleState weightSensor isOnline
  isOnboarded isProvisioned isDebugModeActive lastSeen sessionId setupDateTime
  isFirmwareUpdateTriggered firmwareUpdateStatus wifiModeStatus isUSBPowerOn
  USBFaultStatus isDFIPartialFull isLaserDirty surfaceType hopperStatus
  scoopsSavedCount isHopperRemoved optimalLitterLevel litterLevelPercentage
  litterLevelState
}`

// Command names accepted by sendLitterRobot4Command.
const (
	CmdCleanCycle         = "cleanCycle"
	CmdKeyPadLockOutOff   = "keyPadLockOutOff"
	CmdKeyPadLockOutOn    = "keyPadLockOutOn"
	CmdNightLightModeAuto = "nightLightModeAuto"
	CmdNightLightModeOff  = "nightLightModeOff"
	CmdNightLightModeOn   = "nightLightModeOn"
	CmdPanelBrightnessLow = "panelBrightnessLow"
	CmdPanelBrightnessMed = "panelBrightnessMed"
	CmdPanelBrightnessHi  = "panelBrightnessHigh"
	CmdPowerOff           = "powerOff"
	CmdPowerOn            = "powerOn"
	CmdRequestState       = "requestState"
	CmdSetClumpTime       = "setClumpTime"
	CmdSetNightLightValue = "setNightLightValue"
	CmdShortResetPress    = "shortResetPress"
)

// GraphQLError is an error returned in a GraphQL response.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	return "whisker api: " + strings.Join(e.Messages, "; ")
}

// Client is a Whisker cloud API client.
type Client struct {
	Auth *Auth
	// HTTPClient is used for API requests. http.DefaultClient when nil.
	HTTPClient *http.Client
	// Endpoint overrides Endpoint, for tests.
	Endpoint string
}

// NewClient returns a client that authenticates with auth.
func NewClient(auth *Auth) *Client {
	return &Client{Auth: auth}
}

// Query runs a GraphQL query and decodes its data field into out.
func (c *Client) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	token, err := c.Auth.IDToken(ctx)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, token, query, vars)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.Auth.Refresh(ctx); err != nil {
			return err
		}
		if token, err = c.Auth.IDToken(ctx); err != nil {
			return err
		}
		if resp, err = c.post(ctx, token, query, vars); err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var body struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message   string `json:"message"`
			ErrorType string `json:"errorType"`
		} `json:"errors"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("whisker api: %s", resp.Status)
		}
		return fmt.Errorf("whisker api: %w", err)
	}
	if len(body.Errors) > 0 {
		e := &GraphQLError{}
		for _, m := range body.Errors {
			e.Messages = append(e.Messages, m.Message)
		}
		return e
	}
	if resp.StatusCode != http.StatusOK {
		if body.Message != "" {
			return fmt.Errorf("whisker api: %s: %s", resp.Status, body.Message)
		}
		return fmt.Errorf("whisker api: %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body.Data, out)
}

func (c *Client) post(ctx context.Context, token, query string, vars map[string]any) (*http.Response, error) {
	b, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// RobotData is the raw data the API returns for a robot.
type RobotData map[string]any

// Serial returns the robot's serial number.
func (d RobotData) Serial() string { s, _ := d["serial"].(string); return s }

// Name returns the robot's name.
func (d RobotData) Name() string { s, _ := d["name"].(string); return s }

// Robots lists the Litter-Robot 4 units on the account.
func (c *Client) Robots(ctx context.Context) ([]RobotData, error) {
	token, err := c.Auth.IDToken(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := UserID(token)
	if err != nil {
		return nil, err
	}
	var out struct {
		Robots []RobotData `json:"getLitterRobot4ByUser"`
	}
	q := "query GetLR4($userId: String!) { getLitterRobot4ByUser(userId: $userId) " + robotFields + " }"
	if err := c.Query(ctx, q, map[string]any{"userId": userID}, &out); err != nil {
		return nil, err
	}
	robots := out.Robots[:0]
	for _, r := range out.Robots {
		if r != nil && r.Serial() != "" {
			robots = append(robots, r)
		}
	}
	return robots, nil
}

// RobotBySerial fetches one robot.
func (c *Client) RobotBySerial(ctx context.Context, serial string) (RobotData, error) {
	var out struct {
		Robot RobotData `json:"getLitterRobot4BySerial"`
	}
	q := "query GetLR4($serial: String!) { getLitterRobot4BySerial(serial: $serial) " + robotFields + " }"
	if err := c.Query(ctx, q, map[string]any{"serial": serial}, &out); err != nil {
		return nil, err
	}
	if out.Robot == nil {
		return nil, fmt.Errorf("no Litter-Robot 4 with serial %q on this account", serial)
	}
	return out.Robot, nil
}

// SendCommand sends a command, with an optional JSON value.
func (c *Client) SendCommand(ctx context.Context, serial, command string, value any) error {
	vars := map[string]any{"serial": serial, "command": command, "commandSource": CommandSource}
	if value != nil {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		vars["value"] = string(b)
	}
	var out struct {
		Result *string `json:"sendLitterRobot4Command"`
	}
	q := `mutation sendCommand($serial: String!, $command: String!, $value: String, $commandSource: String) {
  sendLitterRobot4Command(input: {serial: $serial, command: $command, value: $value, commandSource: $commandSource})
}`
	if err := c.Query(ctx, q, vars, &out); err != nil {
		return err
	}
	if out.Result != nil && strings.Contains(*out.Result, "Error") {
		return fmt.Errorf("whisker api: %s", *out.Result)
	}
	return nil
}

// Rename changes a robot's name.
func (c *Client) Rename(ctx context.Context, serial, name string) error {
	q := `mutation rename($serial: String!, $name: String) {
  updateLitterRobot4(input: {serial: $serial, name: $name}) { name }
}`
	return c.Query(ctx, q, map[string]any{"serial": serial, "name": name}, nil)
}

// Activity is one entry in a robot's activity history.
type Activity struct {
	Serial        string `json:"serial"`
	Measure       string `json:"measure"`
	Timestamp     string `json:"timestamp"`
	Value         any    `json:"value"`
	ActionValue   any    `json:"actionValue"`
	OriginalHex   string `json:"originalHex"`
	ValueString   string `json:"valueString"`
	StateString   string `json:"stateString"`
	Consumer      string `json:"consumer"`
	CommandSource string `json:"commandSource"`
}

// Activity returns up to limit history entries between start and end.
func (c *Client) Activity(ctx context.Context, serial string, start, end time.Time, limit int) ([]Activity, error) {
	const layout = "2006-01-02 15:04:05"
	vars := map[string]any{
		"serial":         serial,
		"startTimestamp": start.UTC().Format(layout),
		"endTimestamp":   end.UTC().Format(layout),
		"consumer":       "app",
	}
	if limit > 0 {
		vars["limit"] = limit
	}
	q := `query GetLR4Activity($serial: String!, $startTimestamp: String, $endTimestamp: String, $limit: Int, $consumer: String) {
  getLitterRobot4Activity(serial: $serial, startTimestamp: $startTimestamp, endTimestamp: $endTimestamp, limit: $limit, consumer: $consumer) {
    serial measure timestamp value actionValue originalHex valueString stateString consumer commandSource
  }
}`
	var out struct {
		Activity []Activity `json:"getLitterRobot4Activity"`
	}
	if err := c.Query(ctx, q, vars, &out); err != nil {
		return nil, err
	}
	return out.Activity, nil
}
