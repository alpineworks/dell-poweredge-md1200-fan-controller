# dell-poweredge-md1200-fan-controller

Keeps the fans in a Dell PowerVault MD1200 / MD1220 disk shelf at a fixed,
quiet duty cycle by talking to the Enclosure Management Module (EMM) debug
console over a serial cable. It is a small static Go binary with Prometheus
metrics, meant to run on the host the shelf is cabled to (for example a
PowerEdge running TrueNAS SCALE).

The EMM has no persistent "quiet mode". Its automatic fan profile re-takes
control roughly ten seconds after any manual speed command, and the EMM
periodically restarts itself and forgets everything. The only approach that
has been shown to hold is to **re-send the speed command every one to two
seconds, forever**. That is what this program does, along with a few things
that make it observable and safe.

## What the EMM console is

Each EMM has a 6-pin mini-DIN "password reset" socket on its rear face. It is
a 3.3 V TTL-level UART exposed through Dell's **MN657** password reset /
service cable, which ends in a DB9. Settings are **38400 baud, 8 data bits,
no parity, 1 stop bit**. Interactive terminals (PuTTY, tio, picocom) need
XON/XOFF software flow control turned on to see output; this program instead
uses raw mode with all flow control disabled, which is what every working
script does, so a stray XOFF byte from the EMM can never stall our writes.

Press Enter and you get a prompt whose name encodes the model and firmware:

```
BlueDress.106.000 >        # MD1200, firmware 1.06
RedDress.106.000 >         # MD1220
```

Useful commands (the full list comes from `devils` and `_devils`):

| Command | Effect |
|---|---|
| `_shutup <0-100>` | Force fan duty cycle in percent. Firmware help says "20 default". |
| `set_speed <0-100>` | Identical to `_shutup`. |
| `_temp_rd` | Print every temperature sensor and the average. |
| `set_temp <sensor> <c>` | Override a sensor reading (spoof). Some people use `set_temp 0 0` / `set_temp 1 0` to push the auto profile lower. Lost on EMM restart like everything else. |
| `_ver` | Firmware version and image regions. |
| `_who` | Link, drive, PSU and EMM inventory. The line `EMM (I'm primary and active)` tells you whether the console you are cabled to is the one that controls the fans. |

Example `_temp_rd` output, which this program parses:

```
  BP_1[2] = 20c
  BP_2[3] = 19c
  SIM0[0] = 25c
  SIM1[1] = 25c
  EXP0[4] = 41c
  EXP1[5] = 45c

  AVG = 29c
```

Commands do not survive a power cycle. Nothing is written to flash, so there
is no wear concern with sending a command every second.

## Why the fans ramp back up

Every cause reported in the community, in rough order of how often it bites:

1. **Sending too rarely.** The automatic profile reasserts itself about ten
   seconds after a `_shutup`. A cron job that sends a short burst every 30 or
   60 seconds gives you a quiet second followed by a loud half-minute. Working
   setups send every 1 to 2 seconds continuously. Additionally, a single
   command often does not take; five in quick succession does.
2. **A second EMM is installed.** The primary EMM controls the fans. Commands
   sent to the secondary are overridden, and if the two EMMs run different
   firmware versions the shelf faults and pins the fans at full speed. Fixes:
   pull the second EMM, or cable both consoles and run one instance per EMM
   with both on the same firmware.
3. **Asking for less than 20 %.** The profile floor is 20 % on stock
   firmware. Lower values get overridden within seconds. Use 20 or above.
   Spoofing temperatures with `set_temp` lets some people go lower, at the
   cost of blinding the thermal protection.
4. **The EMM restarts.** Each EMM has a watchdog with a cycle of roughly
   324 seconds; when it fires the host logs
   `ses N:0:0:0: Power-on or device reset occurred`, the console prints
   `**** Devil Startup Complete ... ****`, and all overrides are gone until
   re-sent. This program watches for that banner and immediately re-sends a
   burst.
5. **Something else is on the serial port.** A getty, a kernel console, BIOS
   console redirection, or a second copy of a fan script writing to the same
   tty interleaves bytes with ours. The EMM then sees garbage and answers
   `unknown_cmd`. Some garbled sequences have been observed to land as a
   `set_speed 100`. This program logs and counts `unknown_cmd` replies so you
   can see it happening.
6. **Wrong tty.** On Dell PowerEdge servers the rear DB9 is usually
   `/dev/ttyS1`, not `/dev/ttyS0`. See the R620 section below.
7. **Only one power supply.** The override has been reported to fail on a
   shelf with a single PSU installed. Fit both.
8. **The expanders are hot.** The two LSI expanders (`EXP0`, `EXP1` in
   `_temp_rd`) sit in an airflow dead zone and run 60 °C or more. They are
   what the firmware's thermal loop reacts to, so watch those two sensors,
   not the drive backplane, when picking a speed.

Firmware 1.06 is the last release and its changelog is mostly "optimize
communication between EMMs", so update both EMMs to 1.06 if you keep two.
The effective floor also varies with firmware and thermal state: 20 is safe
everywhere, and some 1.06 units hold 10 or 15.

## Configuration

All settings are environment variables.

| Variable | Default | Meaning |
|---|---|---|
| `SERIAL_PORT` | `/dev/ttyS0` | tty connected to the EMM. Probably `/dev/ttyS1` on a PowerEdge, `/dev/ttyUSB0` for a USB adapter. |
| `SERIAL_BAUDRATE` | `38400` | Leave alone. |
| `SERIAL_DATABITS` | `8` | Leave alone. |
| `COMMAND_TYPE` | `_shutup` | `_shutup` or `set_speed`. |
| `COMMAND_VALUE` | `20` | Fan duty in percent. Below 20 is overridden by firmware. |
| `SEND_INTERVAL` | `2s` | How often the command is re-sent. Keep well under 10 s. |
| `STARTUP_BURST_COUNT` | `5` | Rapid sends on startup and after an EMM restart. |
| `STARTUP_BURST_DELAY` | `300ms` | Gap between burst sends. |
| `TEMP_READ_INTERVAL` | `60s` | How often `_temp_rd` is sent for logging and metrics. `0` disables. |
| `MAX_AVG_TEMP_CELSIUS` | `0` (off) | If the EMM-reported average exceeds this, stop forcing and let the auto profile run until it cools. |
| `MAX_AVG_TEMP_HYSTERESIS` | `3` | Degrees below the limit before forcing resumes. |
| `LOG_LEVEL` | `error` | `debug` shows every console line, including prompts. |
| `METRICS_ENABLED` / `METRICS_PORT` | `true` / `8081` | Prometheus endpoint at `/metrics`. |

The old `CRON_INTERVAL`, `COMMAND_NUM_LOOPS` and `COMMAND_LOOP_DELAY`
variables are ignored and produce a warning at startup.

### Metrics

| Metric | Notes |
|---|---|
| `md1200_commands_sent_total{command}` | Should climb steadily. |
| `md1200_command_errors_total{command}` | Serial write failures. |
| `md1200_emm_restarts_total` | Startup banners seen. |
| `md1200_unknown_commands_total` | `unknown_cmd` replies. Non-zero means port contention or a bad cable. |
| `md1200_temperature_celsius{sensor,index}` | Per sensor from `_temp_rd`. |
| `md1200_average_temperature_celsius` | The `AVG` line. |
| `md1200_fan_target_percent` | What is being forced. |
| `md1200_fan_override_active` | `0` while the thermal cutoff has handed control back. |

## Wiring it to a PowerEdge R620

The R620 has one rear DB9. Which Linux tty it appears as depends on BIOS
settings under **System BIOS → Serial Communication**:

| Setting | Factory default | What you want |
|---|---|---|
| Serial Communication | On without Console Redirection | On without Console Redirection |
| Serial Port Address | Serial Device 1 = COM2, Serial Device 2 = COM1 | Either, but note the mapping |
| External Serial Connector | Serial Device 1 | Serial Device 1 |
| Redirection After Boot | Enabled | Irrelevant while redirection is off |

COM1 is `/dev/ttyS0` and COM2 is `/dev/ttyS1` in Linux. With factory
defaults the rear DB9 is Serial Device 1, which is COM2, which is
**`/dev/ttyS1`**. `/dev/ttyS0` is then Serial Device 2, which is the
internal path to the iDRAC used for Serial Over LAN. Writing to it does
nothing useful. If the console was ever redirected "via COM1/COM2", the BIOS
and iDRAC also chatter on that port, so keep redirection off and set
"External Serial Connector" to a serial device rather than "Remote Access
Device".

Several people could get output but not input from a PowerEdge onboard port
and ended up on a USB-to-RS232 adapter (`/dev/ttyUSB0`). That is a fine
fallback.

Confirm the port before running anything as a service. On the TrueNAS shell:

```sh
stty -F /dev/ttyS1 38400 raw -echo -ixon -ixoff cs8 -cstopb -parenb
cat /dev/ttyS1 &
printf '\r_ver\r' > /dev/ttyS1
sleep 1; printf '\r_temp_rd\r' > /dev/ttyS1
sleep 1; kill %1
```

You should see `BlueDress.` prompts, the version block and the temperature
list. If you see nothing, try `/dev/ttyS0` and the USB adapter. If you see
output but commands are ignored, something else has the port open, or the
cable's TX line is not making contact.

## Running on TrueNAS SCALE

Things to check on the host first:

- **System Settings → Advanced → Console**: *Enable Serial Console* is
  **on by default** in SCALE. Turn it off, or at least point it at a tty the
  shelf is not on. When it is on, TrueNAS starts a login getty on that port
  and adds it as a kernel console, both of which write to the EMM and steal
  its replies. It only accepts onboard `ttyS*` ports, so a USB adapter is
  never at risk from it.
- SCALE ships no `screen`, `minicom` or `picocom`. The `stty` + `printf`
  recipe above is the way to poke the console by hand.
- Nothing else should have the port open: `fuser /dev/ttyS1`.
- If you have two EMMs, decide now whether to pull one or cable both.

### Option A: run the binary from an init script (simplest)

Build a static Linux binary and put it on a pool dataset:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o md1200-fan-controller ./cmd/dell-poweredge-md1200-fan-controller
```

Then in **System Settings → Advanced → Init/Shutdown Scripts** add a
*Post Init* entry of type *Command*:

```sh
SERIAL_PORT=/dev/ttyS1 COMMAND_VALUE=20 LOG_LEVEL=info \
  nohup /mnt/tank/apps/md1200/md1200-fan-controller >> /mnt/tank/apps/md1200/log.json 2>&1 &
```

Set the timeout generously; the command backgrounds itself immediately.

### Option B: Docker (SCALE 24.10 and later)

Use `docker-compose.yml` as a template, or a custom app in the SCALE UI.
The container needs the tty passed through and nothing else special:

```yaml
services:
  md1200:
    image: ghcr.io/alpineworks/dell-poweredge-md1200-fan-controller:latest
    restart: unless-stopped
    devices:
      - /dev/ttyS1:/dev/ttyS1
    environment:
      SERIAL_PORT: /dev/ttyS1
      COMMAND_VALUE: "20"
      LOG_LEVEL: info
    ports:
      - "8081:8081"
```

Only run **one** instance per EMM. Two copies writing to the same tty will
garble each other.

## Verifying it is holding

- `journalctl` / the log file should show `sending fan command burst` once at
  start, then nothing at `info` level except the temperature lines every
  minute and the occasional `EMM restarted` warning.
- `curl localhost:8081/metrics | grep md1200_` should show
  `md1200_unknown_commands_total` at zero and `md1200_commands_sent_total`
  rising by about 30 per minute at the default interval.
- If TrueNAS can see the enclosure, `sg_ses -p es /dev/sgN` (find `N` with
  `lsscsi -g | grep -i enclosu`) reports the actual RPM of each fan
  independently of the serial link.

## Choosing a speed

20 % is the lowest the firmware will hold and is dramatically quieter than
stock. Watch `md1200_average_temperature_celsius` for a day under your real
workload before deciding whether 25 or 30 % is a better trade. The stock
Delta fans are high static pressure units; the drives rely on that pressure
to get air through the backplane. Setting `MAX_AVG_TEMP_CELSIUS` (for
example `40`) gives the firmware its thermal authority back if things get
warm.

## Development

```sh
go test ./...
go build ./...
docker compose up --build   # includes a local Grafana/OTel stack
```
