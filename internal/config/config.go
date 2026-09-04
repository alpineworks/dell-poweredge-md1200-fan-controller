package config

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"error"`

	MetricsEnabled bool `env:"METRICS_ENABLED" envDefault:"true"`
	MetricsPort    int  `env:"METRICS_PORT" envDefault:"8081"`

	Local bool `env:"LOCAL" envDefault:"false"`

	// Serial link to the EMM debug console. The EMM speaks 38400 8N1 and the
	// library puts the port in raw mode (no echo, no XON/XOFF, no RTS/CTS).
	SerialPort     string `env:"SERIAL_PORT" envDefault:"/dev/ttyS0"`
	SerialBaudRate int    `env:"SERIAL_BAUDRATE" envDefault:"38400"`
	SerialDataBits int    `env:"SERIAL_DATABITS" envDefault:"8"`

	// CommandType is the EMM console command used to force fan speed.
	// "_shutup" and "set_speed" behave identically on MD1200/MD1220 firmware.
	CommandType string `env:"COMMAND_TYPE" envDefault:"_shutup"`
	// CommandValue is the fan duty in percent (0-100). The firmware's own
	// default is 20 and values below 20 are overridden by the auto profile.
	CommandValue int `env:"COMMAND_VALUE" envDefault:"20"`

	// SendInterval is how often the fan command is re-sent. The EMM's
	// automatic fan profile re-takes control roughly 10 seconds after a
	// manual command, so this must stay well under that.
	SendInterval time.Duration `env:"SEND_INTERVAL" envDefault:"2s"`
	// StartupBurstCount/Delay: a rapid burst is sent on startup and whenever
	// the EMM is seen restarting, since a single command often does not take.
	StartupBurstCount int           `env:"STARTUP_BURST_COUNT" envDefault:"5"`
	StartupBurstDelay time.Duration `env:"STARTUP_BURST_DELAY" envDefault:"300ms"`

	// TempReadInterval controls how often "_temp_rd" is sent so enclosure
	// temperatures are logged and exported as metrics. 0 disables it.
	TempReadInterval time.Duration `env:"TEMP_READ_INTERVAL" envDefault:"60s"`
	// MaxAvgTempCelsius is a safety cutoff: when the EMM-reported average
	// temperature exceeds it, the controller stops forcing fan speed so the
	// firmware's automatic profile takes over. It resumes once the average
	// drops below (MaxAvgTempCelsius - MaxAvgTempHysteresis). 0 disables.
	MaxAvgTempCelsius    int `env:"MAX_AVG_TEMP_CELSIUS" envDefault:"0"`
	MaxAvgTempHysteresis int `env:"MAX_AVG_TEMP_HYSTERESIS" envDefault:"3"`

	TracingEnabled    bool    `env:"TRACING_ENABLED" envDefault:"false"`
	TracingSampleRate float64 `env:"TRACING_SAMPLERATE" envDefault:"0.01"`
	TracingService    string  `env:"TRACING_SERVICE" envDefault:"dell-poweredge-md1200-fan-controller"`
	TracingVersion    string  `env:"TRACING_VERSION"`
}

// removedEnvVars are settings from earlier versions that no longer do
// anything. They are reported so a stale deployment is not silently misread.
var removedEnvVars = []string{"CRON_INTERVAL", "COMMAND_NUM_LOOPS", "COMMAND_LOOP_DELAY"}

func NewConfig() (*Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	switch c.CommandType {
	case "_shutup", "set_speed":
	default:
		return fmt.Errorf("COMMAND_TYPE must be \"_shutup\" or \"set_speed\", got %q", c.CommandType)
	}
	if c.CommandValue < 0 || c.CommandValue > 100 {
		return fmt.Errorf("COMMAND_VALUE must be between 0 and 100, got %d", c.CommandValue)
	}
	if c.SendInterval <= 0 {
		return fmt.Errorf("SEND_INTERVAL must be positive, got %s", c.SendInterval)
	}
	if c.StartupBurstCount < 0 {
		return fmt.Errorf("STARTUP_BURST_COUNT must not be negative, got %d", c.StartupBurstCount)
	}
	if c.TempReadInterval < 0 {
		return fmt.Errorf("TEMP_READ_INTERVAL must not be negative, got %s", c.TempReadInterval)
	}
	if c.MaxAvgTempCelsius > 0 && c.TempReadInterval == 0 {
		return fmt.Errorf("MAX_AVG_TEMP_CELSIUS requires TEMP_READ_INTERVAL > 0")
	}
	return nil
}

// RemovedEnvVarsSet returns the names of removed settings present in the environment.
func RemovedEnvVarsSet() []string {
	var set []string
	for _, name := range removedEnvVars {
		if _, ok := os.LookupEnv(name); ok {
			set = append(set, name)
		}
	}
	return set
}
