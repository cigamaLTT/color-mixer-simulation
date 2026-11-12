package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type commandPublisher interface {
	Publish(context.Context, string, []byte) error
}

type cachedCommand struct {
	request string
	result  PublishResult
}

type deviceEntry struct {
	telemetry      *Telemetry
	liveAt         time.Time
	lastSeen       time.Time
	reportedOnline bool
	lastEvent      json.RawMessage
	oldSessions    []string
	commandMu      sync.Mutex
	commands       map[string]cachedCommand
	commandOrder   []string
}

type Service struct {
	config    Config
	log       *slog.Logger
	mu        sync.RWMutex
	devices   map[string]*deviceEntry
	broker    BrokerStatus
	publisher commandPublisher
	hub       *socketHub
	now       func() time.Time
}

func newService(config Config, logger *slog.Logger) *Service {
	s := &Service{
		config: config, log: logger, devices: make(map[string]*deviceEntry),
		hub: newSocketHub(), now: time.Now,
		broker: BrokerStatus{Mode: "embedded", TCPAddress: config.MQTTAddr, WSAddress: config.MQTTWSAddr},
	}
	s.devices[config.DeviceID] = &deviceEntry{commands: make(map[string]cachedCommand)}
	if config.MQTTURL != "" {
		s.broker = BrokerStatus{Mode: "external", ExternalURL: publicBrokerURL(config.MQTTURL)}
	}
	return s
}

func (s *Service) setPublisher(p commandPublisher) {
	s.mu.Lock()
	s.publisher = p
	s.mu.Unlock()
}

func (s *Service) setBroker(status BrokerStatus) {
	s.mu.Lock()
	s.broker = status
	if !status.Connected {
		for _, d := range s.devices {
			// Cached or retained readings never become live merely because a broker reconnects.
			d.liveAt = time.Time{}
			d.reportedOnline = false
		}
	}
	s.mu.Unlock()
	s.hub.broadcast(Message{Type: "state", Data: s.snapshot()})
}

func (s *Service) stateLocked(id string, d *deviceEntry, now time.Time) DeviceState {
	st := DeviceState{DeviceID: id, Telemetry: d.telemetry, Stale: true, LastEvent: d.lastEvent}
	if !d.lastSeen.IsZero() {
		t := d.lastSeen.UTC()
		st.LastSeen = &t
	}
	if !d.liveAt.IsZero() {
		age := now.Sub(d.liveAt)
		st.Stale = age > s.config.StaleAfter
		st.Online = s.broker.Connected && d.reportedOnline && age <= s.config.OfflineAfter
	}
	return st
}

func (s *Service) snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	st := Snapshot{
		Devices: make([]DeviceState, 0, len(s.devices)), Broker: s.broker,
		StaleAfterMS: s.config.StaleAfter.Milliseconds(), OfflineAfterMS: s.config.OfflineAfter.Milliseconds(),
	}
	for id, d := range s.devices {
		st.Devices = append(st.Devices, s.stateLocked(id, d, now))
	}
	sort.Slice(st.Devices, func(i, j int) bool { return st.Devices[i].DeviceID < st.Devices[j].DeviceID })
	return st
}

func (s *Service) getOrCreateLocked(id string) *deviceEntry {
	if d := s.devices[id]; d != nil {
		return d
	}
	if len(s.devices) >= 64 {
		return nil
	}
	d := &deviceEntry{commands: make(map[string]cachedCommand)}
	s.devices[id] = d
	return d
}

// ingest receives only device topics. Retained snapshots are displayable but cannot
// establish freshness or authorize a motor command.
func (s *Service) ingest(topic string, payload []byte, retained bool) {
	if len(payload) > 16*1024 {
		return
	}
	parts := strings.Split(topic, "/")
	if len(parts) != 3 || parts[0] != "mixer" || !validDeviceID(parts[1]) {
		return
	}
	id, kind := parts[1], parts[2]
	switch kind {
	case "telemetry":
		t, err := parseTelemetry(payload, id)
		if err != nil {
			s.log.Debug("ignored invalid telemetry", "device_id", id, "error", err)
			return
		}
		s.mu.Lock()
		d := s.getOrCreateLocked(id)
		if d == nil {
			s.mu.Unlock()
			return
		}
		if retained && !d.liveAt.IsZero() {
			s.mu.Unlock()
			return
		}
		if d.telemetry != nil {
			previous := d.telemetry
			if t.SessionID == previous.SessionID {
				if t.Seq < previous.Seq || t.Seq == previous.Seq && (retained || !d.liveAt.IsZero()) {
					s.mu.Unlock()
					return
				}
			} else {
				for _, old := range d.oldSessions {
					if old == t.SessionID {
						s.mu.Unlock()
						return
					}
				}
				d.oldSessions = append(d.oldSessions, previous.SessionID)
				if len(d.oldSessions) > 8 {
					d.oldSessions = d.oldSessions[1:]
				}
			}
		}
		d.telemetry = t
		if !retained {
			d.liveAt, d.lastSeen, d.reportedOnline = s.now(), s.now(), true
		}
		st := s.stateLocked(id, d, s.now())
		s.mu.Unlock()
		s.hub.broadcast(Message{Type: "telemetry", DeviceID: id, Data: t})
		s.hub.broadcast(Message{Type: "status", DeviceID: id, Data: st})
	case "status":
		var p struct {
			DeviceID string `json:"device_id"`
			Online   *bool  `json:"online"`
		}
		if json.Unmarshal(payload, &p) != nil || p.Online == nil || p.DeviceID != "" && p.DeviceID != id {
			return
		}
		s.mu.Lock()
		d := s.getOrCreateLocked(id)
		if d == nil {
			s.mu.Unlock()
			return
		}
		d.reportedOnline = *p.Online
		st := s.stateLocked(id, d, s.now())
		s.mu.Unlock()
		s.hub.broadcast(Message{Type: "status", DeviceID: id, Data: st})
	case "event":
		if retained {
			return
		}
		var event struct {
			Type     string `json:"type"`
			DeviceID string `json:"device_id"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Type == "" || len(event.Type) > 64 || event.DeviceID != "" && event.DeviceID != id {
			return
		}
		raw := append(json.RawMessage(nil), payload...)
		s.mu.Lock()
		d := s.getOrCreateLocked(id)
		if d != nil {
			d.lastEvent = raw
		}
		s.mu.Unlock()
		if d != nil {
			s.hub.broadcast(Message{Type: "event", DeviceID: id, Data: raw})
		}
	}
}

func (s *Service) watchFreshness(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	previous := make(map[string][2]bool)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, st := range s.snapshot().Devices {
				flags := [2]bool{st.Online, st.Stale}
				if prev, ok := previous[st.DeviceID]; !ok || prev != flags {
					s.hub.broadcast(Message{Type: "status", DeviceID: st.DeviceID, Data: st})
				}
				previous[st.DeviceID] = flags
			}
		}
	}
}

func (s *Service) publishCommand(ctx context.Context, req CommandRequest) (PublishResult, *APIError) {
	cmd := req.Command
	if req.Type != "" && req.Type != "command" {
		return PublishResult{}, apiError(http.StatusBadRequest, "invalid_command", "type must be command", cmd.ID)
	}
	if req.DeviceID == "" {
		req.DeviceID = s.config.DeviceID
	}
	if !validDeviceID(req.DeviceID) {
		return PublishResult{}, apiError(http.StatusBadRequest, "invalid_device", "Invalid device identifier", cmd.ID)
	}
	if err := cmd.validate(); err != nil {
		return PublishResult{}, apiError(http.StatusBadRequest, "invalid_command", err.Error(), cmd.ID)
	}
	if cmd.ID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return PublishResult{}, apiError(http.StatusInternalServerError, "internal_error", "Cannot generate command identifier", "")
		}
		cmd.ID = hex.EncodeToString(id[:])
	}
	s.mu.RLock()
	d := s.devices[req.DeviceID]
	s.mu.RUnlock()
	if d == nil {
		return PublishResult{}, apiError(http.StatusConflict, "device_offline", "No live telemetry from this device", cmd.ID)
	}
	d.commandMu.Lock()
	defer d.commandMu.Unlock()
	// Compare the user's intent, excluding server-generated expiry/session fields.
	intent := cmd
	intent.ExpiresUptimeMS, intent.SessionID = 0, ""
	fingerprint, _ := json.Marshal(intent)
	if cached, ok := d.commands[cmd.ID]; ok {
		if cached.request != string(fingerprint) {
			return PublishResult{}, apiError(http.StatusConflict, "command_id_conflict", "This command id was already used for a different command", cmd.ID)
		}
		result := cached.result
		result.Duplicate = true
		return result, nil
	}
	s.mu.RLock()
	st := s.stateLocked(req.DeviceID, d, s.now())
	broker, publisher, liveAt := s.broker, s.publisher, d.liveAt
	s.mu.RUnlock()
	if !broker.Connected || publisher == nil {
		return PublishResult{}, apiError(http.StatusServiceUnavailable, "broker_offline", "MQTT broker is disconnected", cmd.ID)
	}
	if !st.Online || st.Telemetry == nil {
		return PublishResult{}, apiError(http.StatusConflict, "device_offline", "Device is offline; commands are not queued", cmd.ID)
	}
	if cmd.Type != "stop" {
		if st.Stale {
			return PublishResult{}, apiError(http.StatusConflict, "stale_telemetry", "Telemetry is stale; wait for a fresh device reading", cmd.ID)
		}
		if cmd.SessionID != "" && cmd.SessionID != st.Telemetry.SessionID {
			return PublishResult{}, apiError(http.StatusConflict, "session_changed", "Device restarted; refresh the device state", cmd.ID)
		}
		state := st.Telemetry.State
		if state != "idle" && state != "complete" && state != "stopped" && !(cmd.Type == "reset" && state == "fault") {
			return PublishResult{}, apiError(http.StatusConflict, "device_busy", fmt.Sprintf("Cannot %s while device is %s", cmd.Type, state), cmd.ID)
		}
		if cmd.Type == "start" {
			if !st.Telemetry.ScaleValid || st.Telemetry.GrossWeightG == nil {
				return PublishResult{}, apiError(http.StatusConflict, "scale_invalid", "A valid scale reading is required", cmd.ID)
			}
			if st.Telemetry.Fault != nil {
				return PublishResult{}, apiError(http.StatusConflict, "device_fault", "Reset the device fault before starting", cmd.ID)
			}
			if *cmd.TotalG+*st.Telemetry.GrossWeightG > 995 {
				return PublishResult{}, apiError(http.StatusConflict, "scale_capacity", "Requested batch and current cup weight exceed the safe scale capacity", cmd.ID)
			}
		}
		cmd.SessionID = st.Telemetry.SessionID
		ageMS := s.now().Sub(liveAt).Milliseconds()
		if ageMS < 0 {
			ageMS = 0
		}
		cmd.ExpiresUptimeMS = st.Telemetry.UptimeMS + uint64(ageMS) + 5000
	} else {
		cmd.SessionID, cmd.ExpiresUptimeMS = "", 0
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return PublishResult{}, apiError(http.StatusBadRequest, "invalid_command", "Command cannot be encoded", cmd.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.PublishTimeout)
	defer cancel()
	if err := publisher.Publish(ctx, "mixer/"+req.DeviceID+"/command", payload); err != nil {
		message := "MQTT publish failed; device execution is unconfirmed"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "MQTT publish timed out; device execution is unconfirmed"
		}
		s.log.Warn("command publish unconfirmed", "device_id", req.DeviceID, "command_id", cmd.ID)
		return PublishResult{}, apiError(http.StatusServiceUnavailable, "publish_unconfirmed", message, cmd.ID)
	}
	result := PublishResult{
		ID: cmd.ID, DeviceID: req.DeviceID, Status: "published",
		Message: "Published to MQTT; awaiting device acknowledgement",
	}
	d.commands[cmd.ID] = cachedCommand{request: string(fingerprint), result: result}
	d.commandOrder = append(d.commandOrder, cmd.ID)
	if len(d.commandOrder) > 128 {
		delete(d.commands, d.commandOrder[0])
		d.commandOrder = d.commandOrder[1:]
	}
	s.hub.broadcast(Message{Type: "command_ack", DeviceID: req.DeviceID, Data: result})
	return result, nil
}

func apiError(status int, code, message, id string) *APIError {
	return &APIError{Code: code, Message: message, ID: id, status: status}
}
