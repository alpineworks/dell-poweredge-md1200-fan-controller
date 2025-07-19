package config

import (
	"fmt"

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

	TracingEnabled    bool    `env:"TRACING_ENABLED" envDefault:"false"`
	TracingSampleRate float64 `env:"TRACING_SAMPLERATE" envDefault:"0.01"`
	TracingService    string  `env:"TRACING_SERVICE" envDefault:"katalog-agent"`
	TracingVersion    string  `env:"TRACING_VERSION"`

	CronInterval string `env:"CRON_INTERVAL" envDefault:"@every 30s"`
}

func NewConfig() (*Config, error) {
	var cfg Config

	err := env.Parse(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
