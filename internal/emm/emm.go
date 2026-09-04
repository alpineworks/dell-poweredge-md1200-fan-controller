// Package emm talks to the debug console of a Dell PowerVault MD1200/MD1220
// Enclosure Management Module over a serial link.
//
// The console is a plain line-oriented shell (prompt "BlueDress.106.000 >" on
// an MD1200, "RedDress..." on an MD1220) at 38400 8N1. Commands are terminated
// with a carriage return. Responses arrive as CR/LF-separated lines followed
// by a new prompt that has no line terminator of its own.
package emm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

var (
	ErrPortRequired = errors.New("serial port path is required")
	ErrModeRequired = errors.New("serial mode is required")
)

// Fan control commands accepted by the EMM console. Both take a percentage
// (0-100) and behave the same; "20 default" per the firmware help text.
const (
	CommandShutup   = "_shutup"
	CommandSetSpeed = "set_speed"
	CommandTempRead = "_temp_rd"
)

// Line patterns emitted by the EMM console.
var (
	// "  SIM0[0] = 25c"
	tempLineRe = regexp.MustCompile(`^\s*([A-Za-z_0-9]+)\[(\d+)\]\s*=\s*(-?\d+)\s*c\s*$`)
	// "  AVG = 29c"
	avgLineRe = regexp.MustCompile(`^\s*AVG\s*=\s*(-?\d+)\s*c\s*$`)
	// "**** Devil Startup Complete (Based on vendor drop 00.00.63.00) ****"
	startupRe = regexp.MustCompile(`Startup Complete`)
	// "unknown_cmd -> cmd:foo args:1"
	unknownCmdRe = regexp.MustCompile(`^\s*unknown_cmd\b`)
	// "BlueDress.106.000 >" (prompt, possibly with trailing echoed input)
	promptRe = regexp.MustCompile(`^[A-Za-z]+Dress\.[0-9.]+\s*>`)
)

// Temperature is one sensor reading reported by "_temp_rd".
type Temperature struct {
	Sensor  string // e.g. SIM0, BP_1, EXP0
	Index   int
	Celsius int
}

// Event is something noteworthy observed on the console.
type Event struct {
	// Line is the raw text of the console line.
	Line string
	// Temperature is set when the line is a per-sensor temperature.
	Temperature *Temperature
	// AvgCelsius is set (non-nil) when the line is the "_temp_rd" average.
	AvgCelsius *int
	// Restarted is true when the EMM printed its startup banner, meaning any
	// previously forced fan speed or spoofed temperature has been lost.
	Restarted bool
	// UnknownCommand is true when the EMM rejected a command, which usually
	// means bytes were garbled or something else is writing to the port.
	UnknownCommand bool
	// Prompt is true when the line is just the shell prompt.
	Prompt bool
}

// Client is a thread-safe writer to, and line reader from, the EMM console.
type Client struct {
	portPath string
	mode     *serial.Mode
	port     serial.Port
	writeMu  sync.Mutex
}

type Option func(*Client)

func WithPort(path string) Option { return func(c *Client) { c.portPath = path } }
func WithMode(mode *serial.Mode) Option {
	return func(c *Client) { c.mode = mode }
}

// Open opens the serial port and drains anything the EMM has queued up.
func Open(options ...Option) (*Client, error) {
	c := &Client{}
	for _, o := range options {
		o(c)
	}
	if c.portPath == "" {
		return nil, ErrPortRequired
	}
	if c.mode == nil {
		return nil, ErrModeRequired
	}

	port, err := serial.Open(c.portPath, c.mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", c.portPath, err)
	}
	c.port = port

	// A short timeout keeps the reader loop responsive to shutdown; a timed
	// out Read returns (0, nil) in go.bug.st/serial.
	if err := port.SetReadTimeout(500 * time.Millisecond); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("set read timeout: %w", err)
	}
	_ = port.ResetInputBuffer()
	_ = port.ResetOutputBuffer()

	return c, nil
}

func (c *Client) Close() error {
	return c.port.Close()
}

// Send writes one console command. A bare carriage return is sent first so
// that any partial input already sitting in the EMM's line buffer (from a
// previous interrupted write, or a stray byte) is discarded rather than
// prepended to this command.
func (c *Client) Send(command string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	payload := []byte("\r" + command + "\r")
	n, err := c.port.Write(payload)
	if err != nil {
		return fmt.Errorf("write %q: %w", command, err)
	}
	if n != len(payload) {
		return fmt.Errorf("write %q: short write (%d of %d bytes)", command, n, len(payload))
	}
	// Drain() blocks until the UART has actually shifted the bytes out, so a
	// following Send cannot interleave and the EMM sees whole lines.
	if err := c.port.Drain(); err != nil {
		return fmt.Errorf("drain after %q: %w", command, err)
	}
	slog.Debug("sent console command", slog.String("command", command))
	return nil
}

// SetFanSpeed forces the fan duty cycle using the given command ("_shutup" or "set_speed").
func (c *Client) SetFanSpeed(command string, percent int) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("fan speed %d out of range 0-100", percent)
	}
	return c.Send(fmt.Sprintf("%s %d", command, percent))
}

// ReadTemperatures asks the EMM to print its sensors; results arrive
// asynchronously through the reader as Temperature/AvgCelsius events.
func (c *Client) ReadTemperatures() error {
	return c.Send(CommandTempRead)
}

// ReadLoop reads console output until ctx is cancelled, calling handle for
// every complete line and for a dangling prompt once the console goes idle.
func (c *Client) ReadLoop(ctx context.Context, handle func(Event)) error {
	buf := make([]byte, 512)
	var pending []byte

	flush := func(line []byte) {
		text := strings.TrimRight(string(line), "\r\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		handle(ParseLine(text))
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := c.port.Read(buf)
		if err != nil {
			var portErr *serial.PortError
			if errors.As(err, &portErr) && portErr.Code() == serial.PortClosed {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			// Timeout with no data. If a partial line (typically the prompt,
			// which has no terminator) has been waiting, surface it now.
			if len(pending) > 0 {
				flush(pending)
				pending = pending[:0]
			}
			continue
		}

		for _, b := range buf[:n] {
			switch b {
			case '\n', '\r':
				if len(pending) > 0 {
					flush(pending)
					pending = pending[:0]
				}
			default:
				pending = append(pending, b)
			}
		}
	}
}

// ParseLine classifies one line of console output.
func ParseLine(text string) Event {
	ev := Event{Line: text}

	if m := avgLineRe.FindStringSubmatch(text); m != nil {
		v, _ := strconv.Atoi(m[1])
		ev.AvgCelsius = &v
		return ev
	}
	if m := tempLineRe.FindStringSubmatch(text); m != nil {
		idx, _ := strconv.Atoi(m[2])
		v, _ := strconv.Atoi(m[3])
		ev.Temperature = &Temperature{Sensor: m[1], Index: idx, Celsius: v}
		return ev
	}
	if startupRe.MatchString(text) {
		ev.Restarted = true
		return ev
	}
	if unknownCmdRe.MatchString(text) {
		ev.UnknownCommand = true
		return ev
	}
	if promptRe.MatchString(text) {
		ev.Prompt = true
		return ev
	}
	return ev
}
