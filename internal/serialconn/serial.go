package serialconn

import (
	"errors"
	"fmt"
	"log/slog"

	"go.bug.st/serial"
)

var (
	ErrSerialAddressRequired   = errors.New("serial port address is required")
	ErrSerialModeRequired      = errors.New("serial mode is required")
	ErrCouldNotCloseSerialPort = errors.New("could not close serial port")
)

type SerialClient struct {
	portAddress string
	serialMode  *serial.Mode
	port        serial.Port
}

type SerialClientOption func(*SerialClient)

func NewSerialClient(options ...SerialClientOption) (*SerialClient, error) {
	client := &SerialClient{}

	for _, option := range options {
		option(client)
	}

	if client.serialMode == nil {
		return nil, ErrSerialModeRequired
	}

	if client.portAddress == "" {
		return nil, ErrSerialAddressRequired
	}

	port, err := serial.Open(client.portAddress, client.serialMode)
	if err != nil {
		return nil, err
	}

	client.port = port

	return client, nil
}

func WithMode(mode *serial.Mode) SerialClientOption {
	return func(client *SerialClient) {
		client.serialMode = mode
	}
}

func WithPortAddress(address string) SerialClientOption {
	return func(client *SerialClient) {
		client.portAddress = address
	}
}

func (c *SerialClient) Close() error {
	err := c.port.Close()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrCouldNotCloseSerialPort, err.Error())
	}
	return nil
}

func (c *SerialClient) Port() serial.Port {
	return c.port
}

// func (c *SerialClient) SendTemperatureCommand() {
// 	_, err := c.Port().Write([]byte("_temp_rd\r\n"))
// 	if err != nil {
// 		slog.Error("error writing to serial port", slog.String("error", err.Error()))
// 	} else {
// 		slog.Debug("sent temperature read command")
// 	}
// }

func (c *SerialClient) SendCommand(commandType string, value int) {
	_, err := c.Port().Write([]byte(fmt.Sprintf("%s %d\r", commandType, value)))
	if err != nil {
		slog.Error("error writing to serial port", slog.String("error", err.Error()))
	} else {
		slog.Debug("sent command", slog.String("command", commandType), slog.Int("value", value))
	}
}
