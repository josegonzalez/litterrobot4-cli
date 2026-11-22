package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeJWT(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "none"}) + "." + enc(claims) + ".sig"
}

// fakeWhisker is a fake Cognito and GraphQL server.
type fakeWhisker struct {
	t        *testing.T
	mu       sync.Mutex
	logins   int
	refresh  int
	commands []map[string]any
	// expireNext makes the next GraphQL request return 401.
	expireNext bool
	validToken string
	// pending is applied to reported after enough requestState commands.
	pending  map[string]any
	reported map[string]any
	requests int
}

func (f *fakeWhisker) cognito(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := r.Header.Get("X-Amz-Target"); got != "AWSCognitoIdentityProviderService.InitiateAuth" {
		f.t.Errorf("X-Amz-Target = %q", got)
	}
	var req struct {
		AuthFlow       string
		ClientId       string
		AuthParameters map[string]string
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.ClientId != CognitoClientID {
		f.t.Errorf("ClientId = %q", req.ClientId)
	}
	switch req.AuthFlow {
	case "USER_PASSWORD_AUTH":
		if req.AuthParameters["USERNAME"] != "cat@example.com" || req.AuthParameters["PASSWORD"] != "meow" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"__type":"NotAuthorizedException","message":"Incorrect username or password."}`)
			return
		}
		f.logins++
	case "REFRESH_TOKEN_AUTH":
		if req.AuthParameters["REFRESH_TOKEN"] != "refresh-1" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"__type":"NotAuthorizedException","message":"Refresh Token has expired"}`)
			return
		}
		f.refresh++
	default:
		f.t.Errorf("unexpected flow %q", req.AuthFlow)
	}
	f.validToken = fakeJWT(map[string]any{"mid": "user-42", "exp": time.Now().Add(time.Hour).Unix(), "n": f.logins + f.refresh})
	result := map[string]any{"IdToken": f.validToken, "AccessToken": "access", "ExpiresIn": 3600}
	if req.AuthFlow == "USER_PASSWORD_AUTH" {
		result["RefreshToken"] = "refresh-1"
	}
	json.NewEncoder(w).Encode(map[string]any{"AuthenticationResult": result})
}

func (f *fakeWhisker) graphql(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expireNext {
		f.expireNext = false
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"expired"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.validToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Query     string
		Variables map[string]any
	}
	json.NewDecoder(r.Body).Decode(&req)
	switch {
	case strings.Contains(req.Query, "getLitterRobot4ByUser"):
		if req.Variables["userId"] != "user-42" {
			f.t.Errorf("userId = %v", req.Variables["userId"])
		}
		fmt.Fprint(w, `{"data":{"getLitterRobot4ByUser":[
			{"serial":"LR4C000001","name":"Upstairs","isOnline":true,"unitPowerStatus":"ON","robotStatus":"ROBOT_IDLE"},
			{"serial":"","name":"ghost"},
			{"serial":"LR4C000002","name":"Downstairs","isOnline":false}]}}`)
	case strings.Contains(req.Query, "getLitterRobot4BySerial"):
		if req.Variables["serial"] == "missing" {
			fmt.Fprint(w, `{"data":{"getLitterRobot4BySerial":null}}`)
			return
		}
		robot := map[string]any{"serial": req.Variables["serial"], "name": "Upstairs", "isOnline": true, "unitPowerStatus": "ON", "robotStatus": "ROBOT_CLEAN", "nightLightMode": "OFF"}
		for k, v := range f.reported {
			robot[k] = v
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"getLitterRobot4BySerial": robot}})
	case strings.Contains(req.Query, "sendLitterRobot4Command"):
		f.commands = append(f.commands, req.Variables)
		// Like the real cloud, a change only shows up in the state once
		// the robot reports again, here after the second requestState.
		switch req.Variables["command"] {
		case "nightLightModeOn":
			f.pending = map[string]any{"nightLightMode": "ON"}
		case "requestState":
			f.requests++
			if f.requests >= 2 && f.pending != nil {
				f.reported, f.pending = f.pending, nil
			}
		}
		if req.Variables["command"] == "bogus" {
			fmt.Fprint(w, `{"data":{"sendLitterRobot4Command":"Error sending command"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"sendLitterRobot4Command":"command sent"}}`)
	case strings.Contains(req.Query, "broken"):
		fmt.Fprint(w, `{"data":null,"errors":[{"message":"Validation error"}]}`)
	default:
		f.t.Errorf("unexpected query %q", req.Query)
	}
}

func newFake(t *testing.T) (*fakeWhisker, *Client) {
	f := &fakeWhisker{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/cognito", f.cognito)
	mux.HandleFunc("/graphql", f.graphql)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	auth := NewAuth(Tokens{})
	auth.Endpoint = srv.URL + "/cognito"
	c := NewClient(auth)
	c.Endpoint = srv.URL + "/graphql"
	return f, c
}

func TestLoginAndList(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)

	if _, err := c.Auth.Login(ctx, "cat@example.com", "nope"); err == nil || !strings.Contains(err.Error(), "Incorrect username") {
		t.Fatalf("bad password: %v", err)
	}
	tokens, err := c.Auth.Login(ctx, "cat@example.com", "meow")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "refresh-1" || tokens.Expiry.Before(time.Now()) {
		t.Fatalf("tokens: %+v", tokens)
	}

	robots, err := c.Robots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(robots) != 2 || robots[0].Serial() != "LR4C000001" || robots[1].Name() != "Downstairs" {
		t.Fatalf("robots: %v", robots)
	}
	if f.logins != 1 {
		t.Errorf("logins = %d", f.logins)
	}
}

func TestRefreshFromSavedToken(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	c.Auth.tokens = Tokens{RefreshToken: "refresh-1"}
	var saved Tokens
	c.Auth.OnRefresh = func(t Tokens) { saved = t }

	s, err := c.NewRobot("LR4C000001").State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status.Code != "CCP" {
		t.Errorf("status = %v", s.Status)
	}
	if f.refresh != 1 || f.logins != 0 {
		t.Errorf("refresh = %d, logins = %d", f.refresh, f.logins)
	}
	if saved.RefreshToken != "refresh-1" || saved.IDToken == "" {
		t.Errorf("OnRefresh got %+v", saved)
	}

	// A 401 triggers one refresh and a retry.
	f.expireNext = true
	if _, err := c.RobotBySerial(ctx, "LR4C000001"); err != nil {
		t.Fatal(err)
	}
	if f.refresh != 2 {
		t.Errorf("refresh after 401 = %d", f.refresh)
	}
}

func TestExpiredRefreshFallsBackToPassword(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	c.Auth.tokens = Tokens{RefreshToken: "stale"}
	if _, err := c.Robots(ctx); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("without password: %v", err)
	}
	c.Auth.Email, c.Auth.Password = "cat@example.com", "meow"
	if _, err := c.Robots(ctx); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 {
		t.Errorf("logins = %d", f.logins)
	}
}

func TestCommands(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	if _, err := c.Auth.Login(ctx, "cat@example.com", "meow"); err != nil {
		t.Fatal(err)
	}
	r := c.NewRobot("LR4C000001")
	r.ConfirmTimeout = 0
	if err := r.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCleanCycleDelay(ctx, 15); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCleanCycleDelay(ctx, 4); err == nil {
		t.Error("expected an invalid delay error")
	}
	if err := r.SetNightLightBrightness(ctx, "medium"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPanelLock(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SendCommand(ctx, "LR4C000001", "bogus", nil); err == nil || !strings.Contains(err.Error(), "Error sending") {
		t.Errorf("bogus command: %v", err)
	}

	want := []struct{ command, value string }{
		{"cleanCycle", ""},
		{"setClumpTime", `{"clumpTime":15}`},
		{"setNightLightValue", `{"nightLightPower":50}`},
		{"keyPadLockOutOn", ""},
		{"bogus", ""},
	}
	if len(f.commands) != len(want) {
		t.Fatalf("commands: %v", f.commands)
	}
	for i, w := range want {
		got := f.commands[i]
		if got["command"] != w.command || got["serial"] != "LR4C000001" || got["commandSource"] != CommandSource {
			t.Errorf("command %d = %v", i, got)
		}
		v, _ := got["value"].(string)
		if v != w.value {
			t.Errorf("command %d value = %q, want %q", i, v, w.value)
		}
	}
}

func TestCommandWaitsForReportedState(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	if _, err := c.Auth.Login(ctx, "cat@example.com", "meow"); err != nil {
		t.Fatal(err)
	}
	r := c.NewRobot("LR4C000001")
	r.PollInterval = time.Millisecond
	if err := r.SetNightLightMode(ctx, "on"); err != nil {
		t.Fatal(err)
	}
	if f.requests != 2 {
		t.Errorf("requestState sent %d times, want 2", f.requests)
	}
	if s, _ := r.State(ctx); s.NightLightMode != "on" {
		t.Errorf("night light = %q", s.NightLightMode)
	}

	// A change that never shows up times out with ErrNotConfirmed.
	r.ConfirmTimeout = 20 * time.Millisecond
	if err := r.SetPanelLock(ctx, true); !errors.Is(err, ErrNotConfirmed) {
		t.Errorf("SetPanelLock = %v, want ErrNotConfirmed", err)
	}
}

func TestGraphQLErrorsAndMissingRobot(t *testing.T) {
	ctx := context.Background()
	_, c := newFake(t)
	if _, err := c.Auth.Login(ctx, "cat@example.com", "meow"); err != nil {
		t.Fatal(err)
	}
	err := c.Query(ctx, "query broken { x }", nil, nil)
	if _, ok := err.(*GraphQLError); !ok {
		t.Errorf("Query error = %T %v", err, err)
	}
	if _, err := c.RobotBySerial(ctx, "missing"); err == nil {
		t.Error("expected an error for a missing robot")
	}
}

func TestUserID(t *testing.T) {
	if id, err := UserID(fakeJWT(map[string]any{"mid": "abc"})); err != nil || id != "abc" {
		t.Errorf("string mid: %q %v", id, err)
	}
	if id, err := UserID(fakeJWT(map[string]any{"mid": 12345678901.0})); err != nil || id != "12345678901" {
		t.Errorf("numeric mid: %q %v", id, err)
	}
	if _, err := UserID("not-a-jwt"); err == nil {
		t.Error("expected an error")
	}
}
