package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"error"`

	MetricsEnabled bool `env:"METRICS_ENABLED" envDefault:"true"`
	MetricsPort    int  `env:"METRICS_PORT" envDefault:"8081"`

	Local bool `env:"LOCAL" envDefault:"false"`

	SerialPort     string `env:"SERIAL_PORT" envDefault:"/dev/ttyS0"`
	SerialBaudRate int    `env:"SERIAL_BAUDRATE" envDefault:"38400"`
	SerialDataBits int    `env:"SERIAL_DATABITS" envDefault:"8"`
	// SerialStopBits int    `env:"SERIAL_STOPBITS" envDefault:"1"`
	// SerialParity   string `env:"SERIAL_PARITY" envDefault:"none"`

	CronInterval string `env:"CRON_INTERVAL" envDefault:"@every 30s"`

	CommandType      string        `env:"COMMAND_TYPE" envDefault:"_shutup"`         // Command type: "_shutup" or "set_speed"
	CommandNumLoops  int           `env:"COMMAND_NUM_LOOPS" envDefault:"5"`          // Number of times to send the command before stopping
	CommandLoopDelay time.Duration `env:"COMMAND_LOOP_DELAY" envDefault:"200ms"`     // Delay between sending the command
	CommandValue     int           `env:"COMMAND_VALUE" envDefault:"20"`             // Value to send with the command

	TracingEnabled    bool    `env:"TRACING_ENABLED" envDefault:"false"`
	TracingSampleRate float64 `env:"TRACING_SAMPLERATE" envDefault:"0.01"`
	TracingService    string  `env:"TRACING_SERVICE" envDefault:"katalog-agent"`
	TracingVersion    string  `env:"TRACING_VERSION"`
}

func NewConfig() (*Config, error) {
	var cfg Config

	err := env.Parse(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
