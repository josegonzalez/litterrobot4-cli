# litterrobot4-cli

A command line tool to discover and control Whisker Litter-Robot 4 units.

Stock Litter-Robot 4 firmware has no local API: the robot only listens to Whisker's cloud. This tool talks to stock robots through the same reverse-engineered cloud API that the [pylitterbot](https://github.com/natekspencer/pylitterbot) library behind the Home Assistant integration uses, so it needs your Whisker account. Robots flashed with the open source [ESPHome firmware](https://codeberg.org/Joseph-DiGiovanni/esphome-litter-robot) are controlled locally through the ESPHome web server instead.

## Install

Download the binary for your system from the [latest release](https://github.com/josegonzalez/litterrobot4-cli/releases/latest). Binaries are built for Linux, macOS and Windows on amd64 and arm64, and have no dependencies. On Linux and macOS, make the download executable:

```shell
chmod +x litterrobot4-cli_*_darwin_arm64
mv litterrobot4-cli_*_darwin_arm64 /usr/local/bin/litterrobot4-cli
```

macOS may refuse to open a downloaded binary that is not notarized. Clear the quarantine flag with `xattr -d com.apple.quarantine /usr/local/bin/litterrobot4-cli`.

Each release lists SHA-256 checksums, and the binaries have signed build provenance, which you can check with the GitHub CLI:

```shell
gh attestation verify litterrobot4-cli_*_darwin_arm64 --repo josegonzalez/litterrobot4-cli
```

Or build it with Go:

```shell
go install github.com/josegonzalez/litterrobot4-cli@latest
```

`litterrobot4-cli version` prints the installed version.

## Getting started

```shell
litterrobot4-cli setup
```

`setup` asks you to sign in with the email and password you use in the Whisker app, lists the Litter-Robot 4 units on the account, and asks for a name and location for each one that is not saved yet. It then searches the local network. ESPHome robots it finds are saved after it checks their web server password. Stock robots it finds are listed with the command to attach their address to a saved robot. Run it again at any time to pick up new robots.

You can also sign in on its own:

```shell
litterrobot4-cli login
```

Only the session's refresh token is saved by default. When it expires, run `login` again, or use `login --save-password` to also save the password so the tool can sign in again on its own. `logout` removes the account and keeps saved robots.

## Discovery

```shell
litterrobot4-cli discover
```

```
Local network:
  ADDRESS       FIRMWARE  HOSTNAME       MAC  SAVED AS
  192.168.1.50  stock     litter-robot4  -    upstairs-litter-robot

Whisker account:
  SERIAL      NAME                   STATUS  ONLINE  SAVED AS
  LR4C000001  Upstairs Litter Robot  Ready   yes     upstairs-litter-robot
  LR4C000002  Basement Litter Robot  Ready   yes     basement-litter-robot
```

Discovery has three sources:

- ESPHome robots advertise `_esphomelib._tcp` over mDNS.
- Stock robots do not advertise anything and have no open ports. The tool finds them by reverse DNS lookups of every address on the local subnets, which return the `Litter-Robot4` hostname the robot registers with the router over DHCP. A robot that did not register a hostname is not found this way. The MAC address is read from the ARP table where the system allows it. Recent macOS versions hide the table, so the column can be empty.
- The Whisker account lists every robot on it, wherever it is.

Whisker's API does not report a robot's IP or MAC address, so a stock robot on the network cannot be matched to its serial automatically. Stock robots are controlled by serial through the cloud, so their address is only a label. Set it with `config host <name> <address>`.

## Usage

Show a summary of every saved robot:

```shell
litterrobot4-cli status
```

```
NAME                   LOCATION  STATUS  LITTER  WASTE  CAT WEIGHT
basement-litter-robot  Basement  Ready   68%     59%    8.8 lb
upstairs-litter-robot  Office    Ready   66%     69%    7.8 lb
```

Show everything about one robot:

```shell
litterrobot4-cli state --device upstairs
```

Robot commands pick a robot with `--device` (a saved name, serial, host or location), or use the default robot, or the only saved robot. `--serial` works for any robot on the Whisker account without saving it, and `--host` (with `--username` and `--password`) for any ESPHome robot. The `LITTERROBOT_DEVICE`, `LITTERROBOT_SERIAL` and `LITTERROBOT_HOST` environment variables also work.

Start a clean cycle, press reset, or turn a robot on or off:

```shell
litterrobot4-cli clean --device upstairs
litterrobot4-cli reset --device upstairs
litterrobot4-cli power off --device upstairs
```

Change settings:

```shell
litterrobot4-cli set --night-light auto --night-light-brightness medium
litterrobot4-cli set --panel-brightness low --panel-lock on
litterrobot4-cli set --clean-delay 7
```

The clean delay is the wait after a cat leaves before the robot cleans, and must be 3, 7, 15, 25 or 30 minutes.

The Whisker cloud keeps the state a robot last reported, which does not change until the robot reports again. After a command to a cloud robot, the tool asks the robot to report its state and waits until the change shows up, which usually takes several seconds. Use `--wait` to change how long it waits (30 seconds by default) or `--wait 0` to return as soon as the command is sent. If the robot does not report the change in time, the tool prints a warning, since the command may still apply.

Cloud robots also support:

```shell
litterrobot4-cli history --since 48h --limit 20
litterrobot4-cli rename "Upstairs Box"
```

Send a raw command. For cloud robots, the command names are those the Whisker app sends (for example `requestState`, `cleanCycle` or `setClumpTime`). For ESPHome robots, it calls a web server path:

```shell
litterrobot4-cli raw --command setClumpTime --value '{"clumpTime":7}'
litterrobot4-cli raw --host 192.168.1.60 --path "/sensor/Litter Level"
```

All robot commands accept `--json`. `state --raw --json` prints the cloud API's raw data.

## Configuration

```shell
litterrobot4-cli config list
litterrobot4-cli config add upstairs --serial LR4C000001 --location "Living Room"
litterrobot4-cli config add basement --host 192.168.1.60 --password <password>
litterrobot4-cli config location upstairs "Living Room"
litterrobot4-cli config host upstairs 192.168.1.50
litterrobot4-cli config default upstairs
litterrobot4-cli config remove basement
```

`config add` checks the serial against the Whisker account, or connects to the ESPHome robot, before saving. Use `--no-verify` to skip the check. The first robot saved becomes the default. Each location can only be used by one robot.

The account and robots are saved in `$XDG_CONFIG_HOME/litterrobot4-cli/config.toml` (`~/.config/litterrobot4-cli/config.toml` when `XDG_CONFIG_HOME` is unset). The Whisker refresh token, the optional Whisker password and ESPHome passwords are stored in the OS keychain (macOS Keychain, the Linux Secret Service or Windows Credential Manager) under the service `litterrobot4-cli`. When no keychain is usable, or with `--insecure-storage`, they are saved in the config file instead. The config file is created with `0600` permissions.

Without a saved account, cloud commands sign in with the `LITTERROBOT_EMAIL` and `LITTERROBOT_PASSWORD` environment variables.

## ESPHome robots

The ESPHome firmware's example config enables the web server on port 80 with digest auth. The username is `user`, and the password is whatever you set in your YAML. This tool reads and sets the entities the firmware's `litter-robot4` package defines ("Manual Cycle", "Power", "Night Light Mode", "Clean Cycle Delay", "Waste Drawer Level" and so on). The firmware keeps its own robot status internal, so for ESPHome robots the status is worked out from the power switch, the fault sensor, the drawer full sensor and the weight sensor.

## Library

The `litterrobot` package and its `cloud` and `esphome` packages can be used on their own. Both backends implement `litterrobot.Robot`:

```go
auth := cloud.NewAuth(cloud.Tokens{})
if _, err := auth.Login(ctx, email, password); err != nil {
	return err
}
client := cloud.NewClient(auth)
robots, err := client.Robots(ctx)
if err != nil {
	return err
}
robot := client.NewRobot(robots[0].Serial())
state, err := robot.State(ctx)
```

```go
robot, err := esphome.NewClient("192.168.1.60", "user", password)
if err != nil {
	return err
}
err = robot.Clean(ctx)
```

## Releasing

Releases are made by the "Release" workflow. In the Actions tab, open "Release", click "Run workflow" on `main` and pick `patch`, `minor` or `major`, or run:

```shell
gh workflow run release.yml --ref main -f bump=patch
```

The workflow runs CI on Linux, macOS and Windows, and bumps the latest `v*` tag (`v0.0.0` when there is none). It then builds static binaries with `CGO_ENABLED=0` for each platform, attests their build provenance, and creates the tag and a GitHub release with the binaries and a checksum file.

## License

The cloud API details are based on pylitterbot, which is licensed under MIT.
