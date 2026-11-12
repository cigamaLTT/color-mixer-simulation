package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
)

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var commandIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

func validDeviceID(s string) bool { return deviceIDPattern.MatchString(s) }

type Telemetry struct {
	SchemaVersion int             `json:"schema_version"`
	DeviceID      string          `json:"device_id"`
	SessionID     string          `json:"session_id"`
	Seq           uint64          `json:"seq"`
	UptimeMS      uint64          `json:"uptime_ms"`
	State         string          `json:"state"`
	Channel       *int            `json:"channel"`
	WeightG       *float64        `json:"weight_g"`
	GrossWeightG  *float64        `json:"gross_weight_g"`
	TargetG       float64         `json:"target_g"`
	LevelsPct     []float64       `json:"levels_pct"`
	Pumps         []float64       `json:"pumps"`
	Stirrers      []bool          `json:"stirrers"`
	DeliveredG    []float64       `json:"delivered_g"`
	ScaleValid    bool            `json:"scale_valid"`
	Fault         *string         `json:"fault"`
	Simulated     bool            `json:"simulated,omitempty"`
	Debug         json.RawMessage `json:"debug,omitempty"`
}

func parseTelemetry(payload []byte, deviceID string) (*Telemetry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"schema_version", "device_id", "session_id", "seq", "uptime_ms", "state", "channel", "weight_g", "gross_weight_g", "target_g", "levels_pct", "pumps", "stirrers", "delivered_g", "scale_valid", "fault"} {
		v, ok := fields[key]
		if !ok || (bytes.Equal(v, []byte("null")) && key != "channel" && key != "fault" && key != "weight_g" && key != "gross_weight_g") {
			return nil, fmt.Errorf("telemetry missing %s", key)
		}
	}
	var t Telemetry
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, err
	}
	if t.SchemaVersion != 1 || t.DeviceID != deviceID || t.SessionID == "" || len(t.SessionID) > 64 {
		return nil, errors.New("invalid telemetry schema, device or session")
	}
	if !validState(t.State) || t.Channel != nil && (*t.Channel < 0 || *t.Channel > 2) {
		return nil, errors.New("invalid controller state or channel")
	}
	if len(t.LevelsPct) != 3 || len(t.Pumps) != 3 || len(t.Stirrers) != 4 || len(t.DeliveredG) != 3 {
		return nil, errors.New("invalid telemetry channel array lengths")
	}
	if !finite(t.TargetG) || t.TargetG < 0 || t.ScaleValid && (t.WeightG == nil || t.GrossWeightG == nil) {
		return nil, errors.New("invalid scale data")
	}
	for _, p := range []*float64{t.WeightG, t.GrossWeightG} {
		if p != nil && !finite(*p) {
			return nil, errors.New("invalid scale value")
		}
	}
	for _, v := range t.LevelsPct {
		if !finite(v) || v < 0 || v > 100 {
			return nil, errors.New("invalid source level")
		}
	}
	for _, v := range t.Pumps {
		if !finite(v) || v < 0 || v > 1 {
			return nil, errors.New("invalid pump duty")
		}
	}
	for _, v := range t.DeliveredG {
		if !finite(v) || v < 0 {
			return nil, errors.New("invalid delivered mass")
		}
	}
	return &t, nil
}

func validState(s string) bool {
	switch s {
	case "idle", "stirring", "taring", "dosing", "settling", "mixing", "complete", "stopped", "fault":
		return true
	}
	return false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type Command struct {
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	SessionID       string    `json:"session_id,omitempty"`
	ExpiresUptimeMS uint64    `json:"expires_uptime_ms,omitempty"`
	TotalG          *float64  `json:"total_g,omitempty"`
	Ratios          []float64 `json:"ratios,omitempty"`
}

func (c Command) validate() error {
	if c.ID != "" && !commandIDPattern.MatchString(c.ID) {
		return errors.New("command id must be 1–64 letters, digits, dots, colons, underscores or hyphens")
	}
	switch c.Type {
	case "start":
		if c.TotalG == nil || !finite(*c.TotalG) || *c.TotalG < 1 || *c.TotalG > 875 {
			return errors.New("total_g must be between 1 and 875 grams")
		}
		if len(c.Ratios) != 3 {
			return errors.New("ratios must contain green, skin and blue percentages")
		}
		sum := 0.0
		for _, r := range c.Ratios {
			if !finite(r) || r < 0 || r > 100 {
				return errors.New("each ratio must be between 0 and 100")
			}
			sum += r
		}
		if math.Abs(sum-100) > 0.01 {
			return errors.New("ratios must sum to 100 percent")
		}
	case "stop", "reset", "tare":
		if c.TotalG != nil || len(c.Ratios) != 0 {
			return errors.New("only start commands may contain total_g or ratios")
		}
	default:
		return errors.New("command type must be start, stop, reset or tare")
	}
	return nil
}

type CommandRequest struct {
	Type     string  `json:"type,omitempty"`
	DeviceID string  `json:"device_id"`
	Command  Command `json:"command"`
}

type PublishResult struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	ID      string `json:"id,omitempty"`
	status  int
}

func (e *APIError) Error() string { return e.Message }

type DeviceState struct {
	DeviceID  string          `json:"device_id"`
	Telemetry *Telemetry      `json:"telemetry"`
	Online    bool            `json:"online"`
	Stale     bool            `json:"stale"`
	LastSeen  *time.Time      `json:"last_seen"`
	LastEvent json.RawMessage `json:"last_event,omitempty"`
}

type BrokerStatus struct {
	Mode        string `json:"mode"`
	Connected   bool   `json:"connected"`
	TCPAddress  string `json:"tcp_address"`
	WSAddress   string `json:"ws_address"`
	ExternalURL string `json:"external_url,omitempty"`
}

type Snapshot struct {
	Devices        []DeviceState `json:"devices"`
	Broker         BrokerStatus  `json:"broker"`
	StaleAfterMS   int64         `json:"stale_after_ms"`
	OfflineAfterMS int64         `json:"offline_after_ms"`
}

type Message struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id,omitempty"`
	Data     any    `json:"data"`
}
