package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"
)

func testService() *Service {
	return newService(defaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func testTelemetry(id, session string, seq uint64) Telemetry {
	weight, gross := 0.0, 120.0
	return Telemetry{
		SchemaVersion: 1, DeviceID: id, SessionID: session, Seq: seq, UptimeMS: 1000,
		State: "idle", WeightG: &weight, GrossWeightG: &gross, ScaleValid: true,
		LevelsPct: []float64{100, 80, 60}, Pumps: []float64{0, 0, 0},
		Stirrers: []bool{false, false, false, false}, DeliveredG: []float64{0, 0, 0},
	}
}

func ingestTestTelemetry(t *testing.T, s *Service, telemetry Telemetry, retained bool) {
	t.Helper()
	payload, err := json.Marshal(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	s.ingest("mixer/"+telemetry.DeviceID+"/telemetry", payload, retained)
}

func startCommand(id string) CommandRequest {
	total := 100.0
	return CommandRequest{DeviceID: "mixer-01", Command: Command{ID: id, Type: "start", TotalG: &total, Ratios: []float64{50, 30, 20}}}
}

type recordingPublisher struct {
	mu       sync.Mutex
	topics   []string
	payloads [][]byte
	err      error
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return p.err
}

func readyService(t *testing.T) (*Service, *recordingPublisher) {
	t.Helper()
	s := testService()
	p := &recordingPublisher{}
	s.setPublisher(p)
	s.setBroker(BrokerStatus{Mode: "embedded", Connected: true})
	ingestTestTelemetry(t, s, testTelemetry("mixer-01", "boot-one", 1), false)
	return s, p
}

func TestFreshnessDoesNotTrustRetainedOrDuplicateTelemetry(t *testing.T) {
	s := testService()
	now := time.Now()
	s.now = func() time.Time { return now }
	s.setBroker(BrokerStatus{Mode: "external", Connected: true})
	telemetry := testTelemetry("mixer-01", "boot-one", 1)
	ingestTestTelemetry(t, s, telemetry, true)
	state := s.snapshot().Devices[0]
	if state.Telemetry == nil || state.Online || !state.Stale || state.LastSeen != nil {
		t.Fatalf("retained state must be displayable but offline: %+v", state)
	}
	// A live publication of the same cached reading is evidence of a connection.
	ingestTestTelemetry(t, s, telemetry, false)
	if state = s.snapshot().Devices[0]; !state.Online || state.Stale || state.LastSeen == nil {
		t.Fatalf("live state not established: %+v", state)
	}
	now = now.Add(6 * time.Second)
	ingestTestTelemetry(t, s, telemetry, false)
	state = s.snapshot().Devices[0]
	if !state.Online || !state.Stale {
		t.Fatalf("duplicate must not extend freshness: %+v", state)
	}
	now = now.Add(10 * time.Second)
	if state = s.snapshot().Devices[0]; state.Online || !state.Stale {
		t.Fatalf("offline threshold not applied: %+v", state)
	}
	telemetry.Seq++
	ingestTestTelemetry(t, s, telemetry, false)
	s.setBroker(BrokerStatus{Mode: "external", Connected: false})
	s.setBroker(BrokerStatus{Mode: "external", Connected: true})
	if state = s.snapshot().Devices[0]; state.Online || !state.Stale {
		t.Fatalf("broker reconnect must await a live reading: %+v", state)
	}
	ingestTestTelemetry(t, s, telemetry, true)
	if state = s.snapshot().Devices[0]; state.Online || !state.Stale {
		t.Fatal("retained replay authorized commands after broker reconnect")
	}
	telemetry.Seq++
	ingestTestTelemetry(t, s, telemetry, false)
	if state = s.snapshot().Devices[0]; !state.Online || state.Stale {
		t.Fatal("fresh reading did not restore live state")
	}
}

func TestTelemetryRejectsOldSessionsAndSequenceRollback(t *testing.T) {
	s, _ := readyService(t)
	old := testTelemetry("mixer-01", "boot-one", 0)
	ingestTestTelemetry(t, s, old, false)
	if got := s.snapshot().Devices[0].Telemetry.Seq; got != 1 {
		t.Fatalf("sequence rolled back to %d", got)
	}
	ingestTestTelemetry(t, s, testTelemetry("mixer-01", "boot-two", 0), false)
	old.Seq = 99
	ingestTestTelemetry(t, s, old, false)
	if got := s.snapshot().Devices[0].Telemetry.SessionID; got != "boot-two" {
		t.Fatalf("old boot replaced active session: %s", got)
	}
}

func TestCommandsAreBoundToLiveSessionAndDeduplicatedConcurrently(t *testing.T) {
	s, p := readyService(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	ingestTestTelemetry(t, s, testTelemetry("mixer-01", "boot-one", 2), false)
	now = now.Add(1250 * time.Millisecond)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.publishCommand(context.Background(), startCommand("same-id"))
			if err != nil || result.Status != "published" {
				t.Errorf("publish = %+v, %v", result, err)
			}
		}()
	}
	wg.Wait()
	if len(p.payloads) != 1 || p.topics[0] != "mixer/mixer-01/command" {
		t.Fatalf("duplicate publication count = %d", len(p.payloads))
	}
	var cmd Command
	if err := json.Unmarshal(p.payloads[0], &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.SessionID != "boot-one" || cmd.ExpiresUptimeMS != 7250 {
		t.Fatalf("command was not bound to monotonic device time: %+v", cmd)
	}
	conflict := startCommand("same-id")
	*conflict.Command.TotalG = 200
	if _, err := s.publishCommand(context.Background(), conflict); err == nil || err.Code != "command_id_conflict" {
		t.Fatalf("reused id accepted: %v", err)
	}
	// Cached acknowledgement must not republish when a browser retries offline.
	s.setBroker(BrokerStatus{Mode: "embedded", Connected: false})
	result, err := s.publishCommand(context.Background(), startCommand("same-id"))
	if err != nil || !result.Duplicate || len(p.payloads) != 1 {
		t.Fatalf("offline duplicate replayed: %+v, %v", result, err)
	}
}

func TestCommandSafetyGates(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *Telemetry, *CommandRequest)
		code   string
	}{
		{"wrong device", func(_ *Service, _ *Telemetry, r *CommandRequest) { r.DeviceID = "missing" }, "device_offline"},
		{"wildcard device", func(_ *Service, _ *Telemetry, r *CommandRequest) { r.DeviceID = "+" }, "invalid_device"},
		{"too much paint", func(_ *Service, _ *Telemetry, r *CommandRequest) { *r.Command.TotalG = 876 }, "invalid_command"},
		{"wrong ratios", func(_ *Service, _ *Telemetry, r *CommandRequest) { r.Command.Ratios[0] = 40 }, "invalid_command"},
		{"nonfinite", func(_ *Service, _ *Telemetry, r *CommandRequest) { r.Command.Ratios[0] = math.NaN() }, "invalid_command"},
		{"busy", func(_ *Service, v *Telemetry, _ *CommandRequest) { v.State = "dosing" }, "device_busy"},
		{"invalid scale", func(_ *Service, v *Telemetry, _ *CommandRequest) { v.ScaleValid = false }, "scale_invalid"},
		{"cup too heavy", func(_ *Service, v *Telemetry, _ *CommandRequest) { *v.GrossWeightG = 896 }, "scale_capacity"},
		{"fault", func(_ *Service, v *Telemetry, _ *CommandRequest) { fault := "no_flow"; v.Fault = &fault }, "device_fault"},
		{"old session", func(_ *Service, _ *Telemetry, r *CommandRequest) { r.Command.SessionID = "old-boot" }, "session_changed"},
		{"stale", func(s *Service, _ *Telemetry, _ *CommandRequest) {
			now := s.now().Add(6 * time.Second)
			s.now = func() time.Time { return now }
		}, "stale_telemetry"},
		{"offline", func(s *Service, _ *Telemetry, _ *CommandRequest) {
			now := s.now().Add(16 * time.Second)
			s.now = func() time.Time { return now }
		}, "device_offline"},
		{"broker disconnected", func(s *Service, _ *Telemetry, _ *CommandRequest) { s.setBroker(BrokerStatus{Mode: "embedded"}) }, "broker_offline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p := readyService(t)
			telemetry := testTelemetry("mixer-01", "boot-one", 2)
			req := startCommand(tc.name)
			// Use an identifier without spaces; the test exercises the intended gate.
			req.Command.ID = "safety-check"
			tc.change(s, &telemetry, &req)
			// Do not refresh the clock or broker mutations that these cases exercise.
			if tc.name != "stale" && tc.name != "offline" && tc.name != "broker disconnected" {
				ingestTestTelemetry(t, s, telemetry, false)
			}
			if _, err := s.publishCommand(context.Background(), req); err == nil || err.Code != tc.code {
				t.Fatalf("wanted %s, got %v", tc.code, err)
			}
			if len(p.payloads) != 0 {
				t.Fatal("rejected command reached MQTT")
			}
		})
	}
}

func TestStopRemainsAvailableWhileTelemetryIsStale(t *testing.T) {
	s, p := readyService(t)
	now := s.now().Add(6 * time.Second)
	s.now = func() time.Time { return now }
	req := CommandRequest{Command: Command{ID: "stop-now", Type: "stop", SessionID: "obsolete", ExpiresUptimeMS: 1}}
	if _, err := s.publishCommand(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var cmd Command
	if err := json.Unmarshal(p.payloads[0], &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Type != "stop" || cmd.SessionID != "" || cmd.ExpiresUptimeMS != 0 {
		t.Fatalf("stop must not depend on a session or expiry: %+v", cmd)
	}
}

func TestPublishFailureIsUnconfirmedAndNotCached(t *testing.T) {
	s, p := readyService(t)
	p.err = context.DeadlineExceeded
	if _, err := s.publishCommand(context.Background(), startCommand("retry-me")); err == nil || err.Code != "publish_unconfirmed" || err.status != http.StatusServiceUnavailable {
		t.Fatalf("publish failure = %v", err)
	}
	p.err = nil
	result, err := s.publishCommand(context.Background(), startCommand("retry-me"))
	if err != nil || result.Duplicate || len(p.payloads) != 2 {
		t.Fatalf("failed publication was cached: %+v, %v", result, err)
	}
}

func TestTelemetryValidation(t *testing.T) {
	base, err := json.Marshal(testTelemetry("mixer-01", "boot-one", 1))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(base, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schema_version", "device_id", "session_id", "seq", "uptime_ms", "state", "channel", "weight_g", "gross_weight_g", "target_g", "levels_pct", "pumps", "stirrers", "delivered_g", "scale_valid", "fault"} {
		t.Run("missing "+field, func(t *testing.T) {
			value := fields[field]
			delete(fields, field)
			payload, _ := json.Marshal(fields)
			fields[field] = value
			if _, err := parseTelemetry(payload, "mixer-01"); err == nil {
				t.Fatalf("accepted absent %s", field)
			}
		})
	}
	for _, payload := range [][]byte{[]byte("null"), []byte("[]"), []byte(`{"schema_version":1}`)} {
		if _, err := parseTelemetry(payload, "mixer-01"); err == nil {
			t.Fatalf("accepted malformed telemetry %s", payload)
		}
	}
	if _, err := parseTelemetry(base, "different-device"); err == nil {
		t.Fatal("accepted topic/payload device mismatch")
	}
	invalid := testTelemetry("mixer-01", "boot-one", 2)
	invalid.WeightG = nil
	payload, _ := json.Marshal(invalid)
	if _, err := parseTelemetry(payload, "mixer-01"); err == nil {
		t.Fatal("accepted a missing valid scale weight")
	}
	invalid.ScaleValid = false
	payload, _ = json.Marshal(invalid)
	if _, err := parseTelemetry(payload, "mixer-01"); err != nil {
		t.Fatalf("invalid scale may have null weight: %v", err)
	}
}

func TestCommandValidationBoundaries(t *testing.T) {
	for _, mass := range []float64{1, 875} {
		req := startCommand("boundary")
		*req.Command.TotalG = mass
		req.Command.Ratios = []float64{100, 0, 0}
		if err := req.Command.validate(); err != nil {
			t.Fatalf("valid boundary %v rejected: %v", mass, err)
		}
	}
	for _, mass := range []float64{0, 876, math.Inf(1), math.NaN()} {
		req := startCommand("boundary")
		*req.Command.TotalG = mass
		if err := req.Command.validate(); err == nil {
			t.Fatalf("accepted mass %v", mass)
		}
	}
	if err := (Command{ID: "../bad", Type: "stop"}).validate(); err == nil {
		t.Fatal("accepted invalid command identifier")
	}
	if err := (Command{Type: "stop", Ratios: []float64{100, 0, 0}}).validate(); err == nil {
		t.Fatal("accepted a recipe on a stop command")
	}
}
