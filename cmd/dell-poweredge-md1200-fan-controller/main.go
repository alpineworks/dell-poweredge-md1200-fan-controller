package main

import (
	"bufio"
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"alpineworks.io/ootel"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/config"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/logging"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/serialconn"
	"github.com/robfig/cron/v3"
	"go.bug.st/serial"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
)

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "error"
	}

	slogLevel, err := logging.LogLevelToSlogLevel(logLevel)
	if err != nil {
		log.Fatalf("could not convert log level: %s", err)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slogLevel,
	})))
	c, err := config.NewConfig()
	if err != nil {
		slog.Error("could not create config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx := context.Background()

	exporterType := ootel.ExporterTypePrometheus
	if c.Local {
		exporterType = ootel.ExporterTypeOTLPGRPC
	}

	ootelClient := ootel.NewOotelClient(
		ootel.WithMetricConfig(
			ootel.NewMetricConfig(
				c.MetricsEnabled,
				exporterType,
				c.MetricsPort,
			),
		),
		ootel.WithTraceConfig(
			ootel.NewTraceConfig(
				c.TracingEnabled,
				c.TracingSampleRate,
				c.TracingService,
				c.TracingVersion,
			),
		),
	)

	shutdown, err := ootelClient.Init(ctx)
	if err != nil {
		slog.Error("could not create ootel client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	err = runtime.Start(runtime.WithMinimumReadMemStatsInterval(5 * time.Second))
	if err != nil {
		slog.Error("could not create runtime metrics", slog.String("error", err.Error()))
		os.Exit(1)
	}

	err = host.Start()
	if err != nil {
		slog.Error("could not create host metrics", slog.String("error", err.Error()))
		os.Exit(1)
	}

	defer func() {
		_ = shutdown(ctx)
	}()

	serialClient, err := serialconn.NewSerialClient(
		serialconn.WithPortAddress(c.SerialPort),
		serialconn.WithMode(&serial.Mode{
			BaudRate: c.SerialBaudRate,
			DataBits: c.SerialDataBits,
			StopBits: serial.OneStopBit,
			Parity:   serial.NoParity,
		}),
	)
	if err != nil {
		slog.Error("could not create serial client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	defer func() {
		_ = serialClient.Port().Close()
	}()

	slog.Info("starting dell-poweredge-md1200-fan-controller")

	// Set timeouts
	err = serialClient.Port().SetReadTimeout(time.Second * 5)
	if err != nil {
		log.Fatal(err)
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up cron scheduler for periodic command writing
	cronScheduler := cron.New()
	_, err = cronScheduler.AddFunc(c.CronInterval, func() {
		serialClient.SendTemperatureCommand()
		serialClient.SendShutupCommand()
		slog.Info("sent temperature and shutup commands")
	})
	if err != nil {
		slog.Error("could not add cron job", slog.String("error", err.Error()))
		os.Exit(1)
	}

	cronScheduler.Start()
	defer cronScheduler.Stop()

	// Start serial reader with proper goroutine tracking
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		reader := bufio.NewReader(serialClient.Port())
		for {
			select {
			case <-ctx.Done():
				slog.Info("shutting down serial reader")
				return
			default:
				data, err := reader.ReadString('\n')
				if err != nil {
					slog.Error("error reading from serial port", slog.String("error", err.Error()))
					continue
				}

				data = data[:len(data)-1] // Remove trailing newline
				slog.Info("received data from serial port", slog.String("data", data))
			}
		}
	}()

	// Wait for signal or goroutine completion
	sig := <-sigChan
	slog.Info("received signal, shutting down", slog.String("signal", sig.String()))
	cancel()
	wg.Wait()
}
