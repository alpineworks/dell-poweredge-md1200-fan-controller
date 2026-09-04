package emm

import "testing"

func TestParseLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want func(Event) bool
	}{
		{"sensor", "  SIM0[0] = 25c", func(e Event) bool {
			return e.Temperature != nil && e.Temperature.Sensor == "SIM0" && e.Temperature.Index == 0 && e.Temperature.Celsius == 25
		}},
		{"sensor underscore", "  BP_1[2] = 20c", func(e Event) bool {
			return e.Temperature != nil && e.Temperature.Sensor == "BP_1" && e.Temperature.Index == 2 && e.Temperature.Celsius == 20
		}},
		{"sensor spoofed zero", "  SIM1[1] = 0c", func(e Event) bool {
			return e.Temperature != nil && e.Temperature.Celsius == 0
		}},
		{"average", "  AVG = 29c", func(e Event) bool {
			return e.AvgCelsius != nil && *e.AvgCelsius == 29 && e.Temperature == nil
		}},
		{"startup banner", "**** Devil Startup Complete (Based on vendor drop 00.00.63.00) ****", func(e Event) bool {
			return e.Restarted
		}},
		{"unknown command", "unknown_cmd -> cmd:help args:1", func(e Event) bool {
			return e.UnknownCommand
		}},
		{"md1200 prompt", "BlueDress.106.000 >", func(e Event) bool { return e.Prompt }},
		{"md1220 prompt with echo", "RedDress.106.000 >_shutup 20", func(e Event) bool { return e.Prompt }},
		{"plain text", "some other output", func(e Event) bool {
			return !e.Prompt && !e.Restarted && !e.UnknownCommand && e.Temperature == nil && e.AvgCelsius == nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseLine(tt.line)
			if !tt.want(got) {
				t.Errorf("ParseLine(%q) = %+v", tt.line, got)
			}
		})
	}
}
