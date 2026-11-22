package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func setup(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "litterrobot4-cli", "config.toml")
}

func TestRoundTrip(t *testing.T) {
	path := setup(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 0 || c.File() != path {
		t.Fatalf("empty config: %+v", c)
	}

	c.Account = &Account{Email: "cat@example.com"}
	if s, err := c.SetSecret(RefreshTokenKey("cat@example.com"), "refresh", false, &c.Account.RefreshToken); err != nil || s != StorageKeychain {
		t.Fatalf("SetSecret: %v %v", s, err)
	}
	c.Devices["upstairs"] = &Device{Backend: BackendCloud, Serial: "LR4C000001", Location: "Hallway", Host: "192.168.1.50"}
	esp := &Device{Backend: BackendESPHome, Host: "192.168.1.60", MAC: "7C9EBD010203", Username: "user"}
	if s, err := c.SetSecret(DeviceKey("basement", esp), "pw", true, &esp.Password); err != nil || s != StorageFile {
		t.Fatalf("insecure SetSecret: %v %v", s, err)
	}
	c.Devices["basement"] = esp
	c.Default = "upstairs"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("perm: %v %v", info.Mode(), err)
	}

	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Default != "upstairs" || c.Account.Email != "cat@example.com" || c.Account.RefreshToken != "" {
		t.Errorf("loaded: %+v %+v", c, c.Account)
	}
	if v, s, err := c.Secret(RefreshTokenKey("cat@example.com"), c.Account.RefreshToken); v != "refresh" || s != StorageKeychain || err != nil {
		t.Errorf("refresh secret: %q %v %v", v, s, err)
	}
	if v, s, err := c.Secret(DeviceKey("basement", c.Devices["basement"]), c.Devices["basement"].Password); v != "pw" || s != StorageFile || err != nil {
		t.Errorf("device secret: %q %v %v", v, s, err)
	}
	if _, _, err := c.Secret("nothing", ""); !errors.Is(err, ErrNoSecret) {
		t.Errorf("missing secret: %v", err)
	}
}

func TestLookup(t *testing.T) {
	setup(t)
	c, _ := Load()
	c.Devices["upstairs"] = &Device{Backend: BackendCloud, Serial: "LR4C000001", Location: "Hallway", Host: "192.168.1.50"}
	c.Devices["basement"] = &Device{Backend: BackendESPHome, Host: "192.168.1.60", MAC: "7C9EBD010203"}
	cases := map[string]string{
		"upstairs":          "upstairs",
		"lr4c000001":        "upstairs",
		"hallway":           "upstairs",
		"192.168.1.50":      "upstairs",
		"7c:9e:bd:01:02:03": "basement",
		"192.168.1.60":      "basement",
	}
	for key, want := range cases {
		if n, _, ok := c.Lookup(key); !ok || n != want {
			t.Errorf("Lookup(%q) = %q %v, want %q", key, n, ok, want)
		}
	}
	if _, _, ok := c.Lookup("nowhere"); ok {
		t.Error("unexpected match")
	}
	if n, _, ok := c.BySerial("LR4C000001"); !ok || n != "upstairs" {
		t.Errorf("BySerial = %q %v", n, ok)
	}
}

func TestRemoveAndLogout(t *testing.T) {
	setup(t)
	c, _ := Load()
	esp := &Device{Backend: BackendESPHome, Host: "192.168.1.60"}
	c.SetSecret(DeviceKey("basement", esp), "pw", false, &esp.Password)
	c.Devices["basement"] = esp
	c.Default = "basement"
	if err := c.Remove("basement"); err != nil {
		t.Fatal(err)
	}
	if c.Default != "" {
		t.Error("default not cleared")
	}
	if _, err := keyring.Get(KeyringService, DeviceKey("basement", esp)); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("keychain entry left behind: %v", err)
	}

	c.Account = &Account{Email: "Cat@Example.com"}
	c.SetSecret(RefreshTokenKey("Cat@Example.com"), "r", false, &c.Account.RefreshToken)
	c.Logout()
	if c.Account != nil {
		t.Error("account kept")
	}
	if _, err := keyring.Get(KeyringService, RefreshTokenKey("cat@example.com")); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("refresh token left behind: %v", err)
	}
}
