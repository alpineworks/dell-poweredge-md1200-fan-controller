package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"alpineworks.io/ootel"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/config"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/emm"
	"github.com/alpineworks/dell-poweredge-md1200-fan-controller/internal/logging"
	"go.bug.st/serial"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
	if removed := config.RemovedEnvVarsSet(); len(removed) > 0 {
		slog.Warn("ignoring removed settings; use SEND_INTERVAL / STARTUP_BURST_* instead",
			slog.Any("names", removed))
	}
	if c.CommandValue < 20 {
		slog.Warn("fan speed below 20% is overridden by the EMM's automatic profile on stock firmware",
			slog.Int("percent", c.CommandValue))
	}
	if c.SendInterval > 5*time.Second {
		slog.Warn("SEND_INTERVAL above 5s will let the EMM's automatic profile ramp the fans back up between sends",
			slog.Duration("send_interval", c.SendInterval))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exporterType := ootel.ExporterTypePrometheus
	if c.Local {
		exporterType = ootel.ExporterTypeOTLPGRPC
	}

	ootelClient := ootel.NewOotelClient(
		ootel.WithMetricConfig(ootel.NewMetricConfig(c.MetricsEnabled, exporterType, c.MetricsPort)),
		ootel.WithTraceConfig(ootel.NewTraceConfig(c.TracingEnabled, c.TracingSampleRate, c.TracingService, c.TracingVersion)),
	)

	shutdown, err := ootelClient.Init(ctx)
	if err != nil {
		slog.Error("could not create ootel client", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(5 * time.Second)); err != nil {
		slog.Error("could not create runtime metrics", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := host.Start(); err != nil {
		slog.Error("could not create host metrics", slog.String("error", err.Error()))
		os.Exit(1)
	}

	m, err := newMetrics()
	if err != nil {
		slog.Error("could not create metrics", slog.String("error", err.Error()))
		os.Exit(1)
	}

	client, err := emm.Open(
		emm.WithPort(c.SerialPort),
		emm.WithMode(&serial.Mode{
			BaudRate: c.SerialBaudRate,
			DataBits: c.SerialDataBits,
			StopBits: serial.OneStopBit,
			Parity:   serial.NoParity,
		}),
	)
	if err != nil {
		slog.Error("could not open serial port", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	slog.Info("starting dell-poweredge-md1200-fan-controller",
		slog.String("port", c.SerialPort),
		slog.Int("baud", c.SerialBaudRate),
		slog.String("command", c.CommandType),
		slog.Int("percent", c.CommandValue),
		slog.Duration("send_interval", c.SendInterval),
	)

	ctl := &controller{cfg: c, client: client, metrics: m}
	ctl.overrideActive.Store(true)
	ctl.restart = make(chan struct{}, 1)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := client.ReadLoop(ctx, ctl.handleEvent); err != nil && ctx.Err() == nil {
			slog.Error("serial reader stopped", slog.String("error", err.Error()))
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		ctl.sendLoop(ctx)
	}()

	if c.TempReadInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctl.tempLoop(ctx)
		}()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigChan:
		slog.Info("received signal, shutting down", slog.String("signal", sig.String()))
	case <-ctx.Done():
	}
	cancel()
	// Unblock the reader, which may be parked in a 500ms Read.
	_ = client.Close()
	wg.Wait()
}

type metrics struct {
	commandsSent    metric.Int64Counter
	commandErrors   metric.Int64Counter
	emmRestarts     metric.Int64Counter
	unknownCommands metric.Int64Counter
	temperature     metric.Int64Gauge
	avgTemperature  metric.Int64Gauge
	targetSpeed     metric.Int64Gauge
	overrideActive  metric.Int64Gauge
}

func newMetrics() (*metrics, error) {
	meter := otel.Meter("dell-poweredge-md1200-fan-controller")
	var m metrics
	var err error
	if m.commandsSent, err = meter.Int64Counter("md1200_commands_sent_total",
		metric.WithDescription("Console commands written to the EMM")); err != nil {
		return nil, err
	}
	if m.commandErrors, err = meter.Int64Counter("md1200_command_errors_total",
		metric.WithDescription("Console commands that failed to write")); err != nil {
		return nil, err
	}
	if m.emmRestarts, err = meter.Int64Counter("md1200_emm_restarts_total",
		metric.WithDescription("Times the EMM printed its startup banner (forced settings lost)")); err != nil {
		return nil, err
	}
	if m.unknownCommands, err = meter.Int64Counter("md1200_unknown_commands_total",
		metric.WithDescription("Commands the EMM rejected as unknown (garbled input or another writer on the port)")); err != nil {
		return nil, err
	}
	if m.temperature, err = meter.Int64Gauge("md1200_temperature_celsius",
		metric.WithDescription("Enclosure sensor temperature as reported by _temp_rd"),
		metric.WithUnit("Cel")); err != nil {
		return nil, err
	}
	if m.avgTemperature, err = meter.Int64Gauge("md1200_average_temperature_celsius",
		metric.WithDescription("Enclosure average temperature as reported by _temp_rd"),
		metric.WithUnit("Cel")); err != nil {
		return nil, err
	}
	if m.targetSpeed, err = meter.Int64Gauge("md1200_fan_target_percent",
		metric.WithDescription("Fan duty cycle being forced"),
		metric.WithUnit("%")); err != nil {
		return nil, err
	}
	if m.overrideActive, err = meter.Int64Gauge("md1200_fan_override_active",
		metric.WithDescription("1 while the controller is forcing fan speed, 0 while deferring to the EMM automatic profile")); err != nil {
		return nil, err
	}
	return &m, nil
}

type controller struct {
	cfg     *config.Config
	client  *emm.Client
	metrics *metrics

	// overrideActive is false while the safety cutoff has handed control
	// back to the EMM's automatic fan profile.
	overrideActive atomic.Bool
	// restart is signalled when the EMM prints its startup banner so the
	// send loop can immediately re-apply the fan speed.
	restart chan struct{}
}

func (ctl *controller) sendLoop(ctx context.Context) {
	ctl.burst(ctx, "startup")

	ticker := time.NewTicker(ctl.cfg.SendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ctl.restart:
			ctl.burst(ctx, "emm restart")
		case <-ticker.C:
			ctl.sendFanCommand()
		}
	}
}

// burst sends the fan command several times in quick succession. A single
// command frequently does not take on this firmware; five in a row does.
func (ctl *controller) burst(ctx context.Context, reason string) {
	if !ctl.overrideActive.Load() {
		return
	}
	slog.Info("sending fan command burst", slog.String("reason", reason), slog.Int("count", ctl.cfg.StartupBurstCount))
	for i := 0; i < ctl.cfg.StartupBurstCount; i++ {
		ctl.sendFanCommand()
		select {
		case <-ctx.Done():
			return
		case <-time.After(ctl.cfg.StartupBurstDelay):
		}
	}
}

func (ctl *controller) sendFanCommand() {
	ctx := context.Background()
	if !ctl.overrideActive.Load() {
		ctl.metrics.overrideActive.Record(ctx, 0)
		return
	}
	ctl.metrics.overrideActive.Record(ctx, 1)
	ctl.metrics.targetSpeed.Record(ctx, int64(ctl.cfg.CommandValue))

	attrs := metric.WithAttributes(attribute.String("command", ctl.cfg.CommandType))
	if err := ctl.client.SetFanSpeed(ctl.cfg.CommandType, ctl.cfg.CommandValue); err != nil {
		ctl.metrics.commandErrors.Add(ctx, 1, attrs)
		slog.Error("could not send fan command", slog.String("error", err.Error()))
		return
	}
	ctl.metrics.commandsSent.Add(ctx, 1, attrs)
}

func (ctl *controller) tempLoop(ctx context.Context) {
	ticker := time.NewTicker(ctl.cfg.TempReadInterval)
	defer ticker.Stop()

	attrs := metric.WithAttributes(attribute.String("command", emm.CommandTempRead))
	read := func() {
		if err := ctl.client.ReadTemperatures(); err != nil {
			ctl.metrics.commandErrors.Add(ctx, 1, attrs)
			slog.Error("could not request temperatures", slog.String("error", err.Error()))
			return
		}
		ctl.metrics.commandsSent.Add(ctx, 1, attrs)
	}

	// First read shortly after the startup burst has gone out.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		read()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			read()
		}
	}
}

func (ctl *controller) handleEvent(ev emm.Event) {
	ctx := context.Background()
	switch {
	case ev.Temperature != nil:
		t := ev.Temperature
		ctl.metrics.temperature.Record(ctx, int64(t.Celsius),
			metric.WithAttributes(attribute.String("sensor", t.Sensor), attribute.Int("index", t.Index)))
		slog.Info("enclosure temperature", slog.String("sensor", t.Sensor), slog.Int("celsius", t.Celsius))

	case ev.AvgCelsius != nil:
		avg := *ev.AvgCelsius
		ctl.metrics.avgTemperature.Record(ctx, int64(avg))
		slog.Info("enclosure average temperature", slog.Int("celsius", avg))
		ctl.applySafetyCutoff(avg)

	case ev.Restarted:
		ctl.metrics.emmRestarts.Add(ctx, 1)
		slog.Warn("EMM restarted; forced fan speed was lost, re-applying", slog.String("line", ev.Line))
		select {
		case ctl.restart <- struct{}{}:
		default:
		}

	case ev.UnknownCommand:
		ctl.metrics.unknownCommands.Add(ctx, 1)
		slog.Warn("EMM rejected a command; check that nothing else (getty, console redirection, another process) is writing to the serial port",
			slog.String("line", ev.Line))

	case ev.Prompt:
		slog.Debug("console prompt", slog.String("line", ev.Line))

	default:
		slog.Debug("console output", slog.String("line", ev.Line))
	}
}

// applySafetyCutoff hands control back to the EMM's automatic fan profile
// when the enclosure gets too warm, and resumes forcing once it has cooled.
func (ctl *controller) applySafetyCutoff(avg int) {
	limit := ctl.cfg.MaxAvgTempCelsius
	if limit <= 0 {
		return
	}
	active := ctl.overrideActive.Load()
	switch {
	case active && avg > limit:
		ctl.overrideActive.Store(false)
		slog.Warn("average temperature above limit; releasing fans to the EMM automatic profile",
			slog.Int("celsius", avg), slog.Int("limit", limit))
	case !active && avg <= limit-ctl.cfg.MaxAvgTempHysteresis:
		ctl.overrideActive.Store(true)
		slog.Info("average temperature back below limit; resuming forced fan speed",
			slog.Int("celsius", avg), slog.Int("resume_below", limit-ctl.cfg.MaxAvgTempHysteresis))
		select {
		case ctl.restart <- struct{}{}:
		default:
		}
	}
}
