// Package config stores the Whisker account and saved robots in the XDG
// config home, and their secrets in the OS keychain, falling back to the
// config file when no keychain is usable.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

// KeyringService is the service name secrets are stored under.
const KeyringService = "litterrobot4-cli"

// Backends.
const (
	BackendCloud   = "cloud"
	BackendESPHome = "esphome"
)

// ErrNoSecret is returned when a secret is not saved anywhere.
var ErrNoSecret = errors.New("secret not saved")

// Account is the Whisker account used for cloud robots.
type Account struct {
	Email string `toml:"email"`
	// RefreshToken and Password are set only when stored in the file.
	RefreshToken string `toml:"refresh_token,omitempty"`
	Password     string `toml:"password,omitempty"`
}

// Device is a saved robot.
type Device struct {
	// Backend is BackendCloud (stock firmware, controlled through the
	// Whisker cloud by serial) or BackendESPHome (controlled locally).
	Backend  string `toml:"backend"`
	Serial   string `toml:"serial,omitempty"`
	Host     string `toml:"host,omitempty"`
	MAC      string `toml:"mac,omitempty"`
	Location string `toml:"location,omitempty"`
	// Username is the ESPHome web server user.
	Username string `toml:"username,omitempty"`
	// Password is set only when the ESPHome password is stored in the file.
	Password string `toml:"password,omitempty"`
}

// Config is the contents of the config file.
type Config struct {
	Default string             `toml:"default,omitempty"`
	Account *Account           `toml:"account,omitempty"`
	Devices map[string]*Device `toml:"devices,omitempty"`

	path string
}

// Path returns the config file path: $XDG_CONFIG_HOME/litterrobot4-cli/config.toml,
// or ~/.config/litterrobot4-cli/config.toml when XDG_CONFIG_HOME is unset.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "litterrobot4-cli", "config.toml"), nil
}

// Load reads the config file. A missing file yields an empty config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	c := &Config{path: path, Devices: map[string]*Device{}}

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	// Windows does not have Unix permissions and reports every file as
	// readable by everyone, so the check only applies elsewhere.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "warning: %s is accessible by other users; run: chmod 600 %s\n", path, path)
	}
	if _, err := toml.DecodeFile(path, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if c.Devices == nil {
		c.Devices = map[string]*Device{}
	}
	return c, nil
}

// File returns the path the config was loaded from.
func (c *Config) File() string { return c.path }

// Save writes the config file atomically with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// Names returns the saved device names, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Devices))
	for n := range c.Devices {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Lookup finds a device by name, then by serial, host, MAC or location.
func (c *Config) Lookup(key string) (string, *Device, bool) {
	if d, ok := c.Devices[key]; ok {
		return key, d, true
	}
	for _, n := range c.Names() {
		d := c.Devices[n]
		if (d.Serial != "" && strings.EqualFold(d.Serial, key)) ||
			(d.Host != "" && strings.EqualFold(d.Host, key)) ||
			(d.MAC != "" && NormalizeMAC(d.MAC) == NormalizeMAC(key)) ||
			(d.Location != "" && strings.EqualFold(d.Location, key)) {
			return n, d, true
		}
	}
	return "", nil, false
}

// BySerial finds a cloud device by serial.
func (c *Config) BySerial(serial string) (string, *Device, bool) {
	for _, n := range c.Names() {
		if d := c.Devices[n]; d.Backend == BackendCloud && strings.EqualFold(d.Serial, serial) {
			return n, d, true
		}
	}
	return "", nil, false
}

// NormalizeMAC uppercases a MAC address and strips separators.
func NormalizeMAC(mac string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
}

// Storage describes where a secret is stored.
type Storage string

// Secret storage locations.
const (
	StorageKeychain Storage = "keychain"
	StorageFile     Storage = "config file"
)

// SetSecret stores a secret in the keychain under key, or in *field
// (saved with the config file) when the keychain is unusable or insecure
// is true. The config must be saved afterwards.
func (c *Config) SetSecret(key, value string, insecure bool, field *string) (Storage, error) {
	if !insecure {
		err := keyring.Set(KeyringService, key, value)
		if err == nil {
			*field = ""
			return StorageKeychain, nil
		}
		fmt.Fprintf(os.Stderr, "warning: keychain unavailable (%v); storing secret in %s\n", err, c.path)
	}
	*field = value
	return StorageFile, nil
}

// Secret returns a secret from the file field, or from the keychain.
func (c *Config) Secret(key, field string) (string, Storage, error) {
	if field != "" {
		return field, StorageFile, nil
	}
	v, err := keyring.Get(KeyringService, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", "", ErrNoSecret
		}
		return "", "", fmt.Errorf("read %s from keychain: %w", key, err)
	}
	return v, StorageKeychain, nil
}

// DeleteSecret removes a secret from the keychain, ignoring missing ones.
func DeleteSecret(key string) {
	if err := keyring.Delete(KeyringService, key); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "warning: could not remove keychain entry %s: %v\n", key, err)
	}
}

// RefreshTokenKey is the keychain key for the account's refresh token.
func RefreshTokenKey(email string) string { return "whisker-refresh:" + strings.ToLower(email) }

// PasswordKey is the keychain key for the account's password.
func PasswordKey(email string) string { return "whisker-password:" + strings.ToLower(email) }

// DeviceKey is the keychain key for an ESPHome device's password.
func DeviceKey(name string, d *Device) string {
	if d.MAC != "" {
		return "esphome:" + NormalizeMAC(d.MAC)
	}
	return "esphome:name:" + name
}

// Remove deletes a device and its keychain entry.
func (c *Config) Remove(name string) error {
	d, ok := c.Devices[name]
	if !ok {
		return fmt.Errorf("no device named %q", name)
	}
	if d.Backend == BackendESPHome && d.Password == "" {
		DeleteSecret(DeviceKey(name, d))
	}
	delete(c.Devices, name)
	if c.Default == name {
		c.Default = ""
	}
	return nil
}

// Logout removes the account and its keychain entries. Cloud devices stay
// saved so signing in again restores them.
func (c *Config) Logout() {
	if c.Account == nil {
		return
	}
	DeleteSecret(RefreshTokenKey(c.Account.Email))
	DeleteSecret(PasswordKey(c.Account.Email))
	c.Account = nil
}
