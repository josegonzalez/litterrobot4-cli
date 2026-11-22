package esphome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/josegonzalez/litterrobot4-cli/litterrobot"
)

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// fakeDevice is a fake ESPHome web server with digest auth. It only
// accepts object id URLs for "Litter Level", to exercise the fallback.
type fakeDevice struct {
	t        *testing.T
	mu       sync.Mutex
	user     string
	pass     string
	posts    []string
	entities map[string]string // "domain/name" -> JSON
}

const realm = "asyncesp"
const nonce = "abc123"

func (f *fakeDevice) checkAuth(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Digest ") {
		return false
	}
	p := parseChallenge(strings.TrimPrefix(h, "Digest "))
	ha1 := md5hex(f.user + ":" + realm + ":" + f.pass)
	ha2 := md5hex(r.Method + ":" + p["uri"])
	want := md5hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":" + p["qop"] + ":" + ha2)
	return p["username"] == f.user && p["response"] == want && p["uri"] == r.URL.RequestURI()
}

func (f *fakeDevice) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkAuth(r) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", opaque="xyz"`, realm, nonce))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	key := parts[0] + "/" + parts[1]
	if r.Method == http.MethodPost {
		action := ""
		if len(parts) == 3 {
			action = parts[2]
		}
		f.posts = append(f.posts, key+"/"+action+"?"+r.URL.RawQuery)
		return
	}
	body, ok := f.entities[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, body)
}

func newDevice(t *testing.T) (*fakeDevice, *Client) {
	f := &fakeDevice{t: t, user: "user", pass: "s3cret", entities: map[string]string{
		"switch/Power":                       `{"id":"switch-power","state":"ON","value":true}`,
		"switch/Control Panel Lockout":       `{"id":"switch-control_panel_lockout","state":"OFF","value":false}`,
		"sensor/Waste Drawer Level":          `{"id":"sensor-waste_drawer_level","state":"42.0 %","value":42}`,
		"sensor/litter_level":                `{"id":"sensor-litter_level","state":"88 %","value":88}`,
		"sensor/Last Cat Weight":             `{"id":"sensor-last_cat_weight","state":"10.2 lbs","value":10.2}`,
		"number/Clean Cycle Delay":           `{"id":"number-clean_cycle_delay","state":"7","value":"7"}`,
		"binary_sensor/Waste Drawer Full":    `{"id":"binary_sensor-waste_drawer_full","state":"OFF","value":false}`,
		"binary_sensor/Weight Detected":      `{"id":"binary_sensor-weight_detected","state":"ON","value":true}`,
		"text_sensor/Fault":                  `{"id":"text_sensor-fault","state":"None","value":"None"}`,
		"text_sensor/Power Type":             `{"id":"text_sensor-power_type","state":"AC","value":"AC"}`,
		"select/Night Light Mode":            `{"id":"select-night_light_mode","state":"Auto","value":"Auto","option":["Off","On","Auto"]}`,
		"select/Night Light Brightness":      `{"id":"select-night_light_brightness","state":"Med","value":"Med","option":["Low","Med","High"]}`,
		"select/Panel Brightness":            `{"id":"select-panel_brightness","state":"High","value":"High","option":["Low","Medium","High"]}`,
		"binary_sensor/Sleeping":             `{"id":"binary_sensor-sleeping","state":"OFF","value":false}`,
		"binary_sensor/Curtain Detected":     `{"id":"binary_sensor-curtain_detected","state":"OFF","value":false}`,
		"sensor/Clean Cycles":                `{"id":"sensor-clean_cycles","state":"321","value":321}`,
		"text_sensor/Robot Status (missing)": `{}`,
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "user", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestState(t *testing.T) {
	_, c := newDevice(t)
	s, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !*s.Power || *s.PanelLock || *s.WasteLevel != 42 || *s.LitterLevel != 88 || *s.CatWeight != 10.2 || *s.CleanCycleDelay != 7 || *s.CleanCycles != 321 {
		t.Errorf("state: %+v", s)
	}
	if s.NightLightMode != litterrobot.NightLightAuto || s.NightLightBrightness != litterrobot.BrightnessMedium || s.PanelBrightness != litterrobot.BrightnessHigh {
		t.Errorf("lights: %+v", s)
	}
	if s.PowerType != "AC" || s.Status != litterrobot.StatusCatDetected {
		t.Errorf("status %v, power type %q", s.Status, s.PowerType)
	}
}

func TestCommands(t *testing.T) {
	f, c := newDevice(t)
	ctx := context.Background()
	steps := []func() error{
		func() error { return c.Clean(ctx) },
		func() error { return c.SetPower(ctx, false) },
		func() error { return c.SetPanelLock(ctx, true) },
		func() error { return c.SetNightLightMode(ctx, litterrobot.NightLightOff) },
		func() error { return c.SetNightLightBrightness(ctx, litterrobot.BrightnessMedium) },
		func() error { return c.SetPanelBrightness(ctx, litterrobot.BrightnessMedium) },
		func() error { return c.SetCleanCycleDelay(ctx, 15) },
	}
	for i, s := range steps {
		if err := s(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := []string{
		"button/Manual Cycle/press?",
		"switch/Power/turn_off?",
		"switch/Control Panel Lockout/turn_on?",
		"select/Night Light Mode/set?option=Off",
		"select/Night Light Brightness/set?option=Med",
		"select/Panel Brightness/set?option=Medium",
		"number/Clean Cycle Delay/set?value=15",
	}
	if strings.Join(f.posts, "\n") != strings.Join(want, "\n") {
		t.Errorf("posts:\n%s\nwant:\n%s", strings.Join(f.posts, "\n"), strings.Join(want, "\n"))
	}
}

func TestWrongPassword(t *testing.T) {
	f, _ := newDevice(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	c, _ := NewClient(srv.URL, "user", "wrong")
	if err := c.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("Ping = %v", err)
	}
}

func TestObjectID(t *testing.T) {
	cases := map[string]string{
		"Manual Cycle":          "manual_cycle",
		"Control Panel Lockout": "control_panel_lockout",
		"Litter Level":          "litter_level",
		"WasteSense (IAQ)":      "wastesense_iaq",
	}
	for in, want := range cases {
		if got := ObjectID(in); got != want {
			t.Errorf("ObjectID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	got := parseChallenge(`realm="a, b", nonce="n", qop="auth,auth-int", algorithm=MD5, opaque="o"`)
	want := map[string]string{"realm": "a, b", "nonce": "n", "qop": "auth,auth-int", "algorithm": "MD5", "opaque": "o"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
