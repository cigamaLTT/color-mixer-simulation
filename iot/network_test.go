package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"
)

type mqttPublication struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func eventually(t *testing.T, description string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + description)
}

func testBroker(t *testing.T) (*Service, *embeddedBroker) {
	t.Helper()
	s := testService()
	s.config.MQTTAddr, s.config.MQTTWSAddr = "127.0.0.1:0", "127.0.0.1:0"
	b, err := startEmbeddedBroker(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return s, b
}

func waitToken(t *testing.T, token paho.Token) {
	t.Helper()
	if !token.WaitTimeout(4 * time.Second) {
		t.Fatal("MQTT operation timed out")
	}
	if err := token.Error(); err != nil {
		t.Fatal(err)
	}
}

func connectTestMQTT(t *testing.T, uri, id string, messages chan<- mqttPublication) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().AddBroker(uri).SetClientID(id).
		SetProtocolVersion(4).SetCleanSession(true).SetAutoReconnect(false).
		SetConnectTimeout(2 * time.Second).SetWriteTimeout(time.Second)
	if messages != nil {
		opts.SetDefaultPublishHandler(func(_ paho.Client, msg paho.Message) {
			message := mqttPublication{msg.Topic(), append([]byte(nil), msg.Payload()...), msg.Retained(), msg.Qos()}
			select {
			case messages <- message:
			default:
			}
		})
	}
	client := paho.NewClient(opts)
	waitToken(t, client.Connect())
	t.Cleanup(func() { client.Disconnect(50) })
	return client
}

func nextMQTT(t *testing.T, messages <-chan mqttPublication, topic string) mqttPublication {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-messages:
			if msg.topic == topic {
				return msg
			}
		case <-deadline.C:
			t.Fatal("no MQTT publication on " + topic)
			return mqttPublication{}
		}
	}
}

func nextSocketMessage(t *testing.T, conn *websocket.Conn, kind string) Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == kind {
			return msg
		}
	}
}

func TestRealMQTTTCPWebSocketAndApplicationRoutes(t *testing.T) {
	s, broker := testBroker(t)
	server := httptest.NewServer(s.handler(nil))
	defer server.Close()
	defer s.hub.close()
	appSocket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer appSocket.Close()
	_ = nextSocketMessage(t, appSocket, "state")
	tcp := connectTestMQTT(t, "tcp://"+broker.status.TCPAddress, "test-tcp", nil)
	messages := make(chan mqttPublication, 32)
	mqttWS := connectTestMQTT(t, "ws://"+broker.status.WSAddress, "test-mqtt-ws", messages)
	telemetryTopic, commandTopic := "mixer/mixer-01/telemetry", "mixer/mixer-01/command"
	waitToken(t, mqttWS.SubscribeMultiple(map[string]byte{telemetryTopic: 0, commandTopic: 1}, nil))
	payload, _ := json.Marshal(testTelemetry("mixer-01", "socket-boot", 1))
	waitToken(t, tcp.Publish(telemetryTopic, 1, false, payload))
	if got := nextMQTT(t, messages, telemetryTopic); !bytes.Equal(got.payload, payload) {
		t.Fatalf("MQTT WS changed telemetry: %s", got.payload)
	}
	_ = nextSocketMessage(t, appSocket, "telemetry")
	eventually(t, "live telemetry", func() bool { return s.snapshot().Devices[0].Online })
	request, _ := json.Marshal(startCommand("http-start"))
	response, err := http.Post(server.URL+"/api/command", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("HTTP command status %d", response.StatusCode)
	}
	publication := nextMQTT(t, messages, commandTopic)
	var cmd Command
	if err := json.Unmarshal(publication.payload, &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ID != "http-start" || cmd.SessionID != "socket-boot" || cmd.ExpiresUptimeMS < 6000 || publication.retained || publication.qos != 1 {
		t.Fatalf("invalid MQTT command: %+v, retained=%v qos=%d", cmd, publication.retained, publication.qos)
	}
	_ = nextSocketMessage(t, appSocket, "command_ack")
	waitToken(t, tcp.Publish("mixer/mixer-01/event", 1, false, `{"id":"http-start","type":"command_ack","accepted":true,"state":"stirring"}`))
	_ = nextSocketMessage(t, appSocket, "event")
	if err := appSocket.WriteJSON(CommandRequest{Type: "command", DeviceID: "mixer-01", Command: Command{ID: "ws-stop", Type: "stop"}}); err != nil {
		t.Fatal(err)
	}
	publication = nextMQTT(t, messages, commandTopic)
	// Decode into a fresh value: omitted fields must not inherit the start command.
	var stop Command
	_ = json.Unmarshal(publication.payload, &stop)
	if stop.Type != "stop" || stop.SessionID != "" || stop.ExpiresUptimeMS != 0 || publication.retained {
		t.Fatalf("invalid stop command: %s", publication.payload)
	}
	_ = nextSocketMessage(t, appSocket, "command_ack")
	// Reconnect a subscriber after both commands. Neither command may be replayed.
	lateMessages := make(chan mqttPublication, 4)
	late := connectTestMQTT(t, "tcp://"+broker.status.TCPAddress, "late-device", lateMessages)
	waitToken(t, late.Subscribe(commandTopic, 1, nil))
	select {
	case msg := <-lateMessages:
		t.Fatalf("command replayed to a late subscriber: %s", msg.payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBrokerRejectsRetainedCommandsAndPersistentSessions(t *testing.T) {
	_, broker := testBroker(t)
	uri := "tcp://" + broker.status.TCPAddress
	commandTopic := "mixer/mixer-01/command"
	messages := make(chan mqttPublication, 4)
	observer := connectTestMQTT(t, uri, "command-observer", messages)
	waitToken(t, observer.Subscribe(commandTopic, 1, nil))
	publisher := connectTestMQTT(t, uri, "retained-publisher", nil)
	// QoS 0 permits checking rejection without waiting for an acknowledgement
	// which MQTT 3.1.1 cannot use to represent this publication error.
	waitToken(t, publisher.Publish(commandTopic, 0, true, `{"id":"unsafe","type":"start"}`))
	// SUBACK on the same transport ensures the broker processed the earlier PUBLISH.
	waitToken(t, publisher.Subscribe("test/flush", 0, nil))
	late := connectTestMQTT(t, uri, "retained-observer", messages)
	waitToken(t, late.Subscribe(commandTopic, 1, nil))
	select {
	case msg := <-messages:
		t.Fatalf("retained actuator command was delivered: %s", msg.payload)
	case <-time.After(100 * time.Millisecond):
	}
	persistent := paho.NewClient(paho.NewClientOptions().AddBroker(uri).
		SetClientID("persistent-device").SetCleanSession(false).
		SetAutoReconnect(false).SetConnectTimeout(time.Second))
	defer persistent.Disconnect(0)
	token := persistent.Connect()
	if !token.WaitTimeout(3*time.Second) || token.Error() == nil {
		t.Fatal("persistent actuator session was accepted")
	}
	willClient := paho.NewClient(paho.NewClientOptions().AddBroker(uri).
		SetClientID("command-will").SetCleanSession(true).SetAutoReconnect(false).
		SetWill(commandTopic, `{"id":"delayed","type":"start"}`, 1, true).
		SetConnectTimeout(time.Second))
	defer willClient.Disconnect(0)
	token = willClient.Connect()
	if !token.WaitTimeout(3*time.Second) || token.Error() == nil {
		t.Fatal("retained actuator Last Will was accepted")
	}
}

func TestExternalBrokerReconnectRequiresFreshTelemetryAndNeverQueuesCommands(t *testing.T) {
	_, upstream := testBroker(t)
	external := testService()
	external.config.MQTTURL = "tcp://" + upstream.status.TCPAddress
	ctx, cancel := context.WithCancel(context.Background())
	bridge := startExternalBroker(ctx, external)
	defer func() { cancel(); _ = bridge.Close() }()
	eventually(t, "external broker connection", func() bool { return external.snapshot().Broker.Connected })
	device := connectTestMQTT(t, external.config.MQTTURL, "external-device", nil)
	telemetry := testTelemetry("mixer-01", "external-boot", 1)
	payload, _ := json.Marshal(telemetry)
	waitToken(t, device.Publish("mixer/mixer-01/telemetry", 1, true, payload))
	eventually(t, "external live telemetry", func() bool { return external.snapshot().Devices[0].Online })
	oldAddress := upstream.status.TCPAddress
	if err := upstream.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "external disconnect", func() bool { return !external.snapshot().Broker.Connected })
	if _, err := external.publishCommand(context.Background(), startCommand("during-outage")); err == nil || err.Code != "broker_offline" {
		t.Fatalf("disconnected command was queued: %v", err)
	}
	replacement := testService()
	replacement.config.MQTTAddr, replacement.config.MQTTWSAddr = oldAddress, "127.0.0.1:0"
	restarted, err := startEmbeddedBroker(replacement)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	// The bridge waits two seconds before reconnecting. Seed a retained snapshot
	// on the replacement broker before it subscribes again.
	telemetry.Seq++
	payload, _ = json.Marshal(telemetry)
	waitToken(t, connectTestMQTT(t, external.config.MQTTURL, "restarted-device", nil).
		Publish("mixer/mixer-01/telemetry", 1, true, payload))
	eventually(t, "bridge reconnect", func() bool { return external.snapshot().Broker.Connected })
	eventually(t, "retained snapshot", func() bool { return external.snapshot().Devices[0].Telemetry.Seq == 2 })
	state := external.snapshot().Devices[0]
	if state.Online || !state.Stale {
		t.Fatalf("retained telemetry became live after reconnect: %+v", state)
	}
	if _, err := external.publishCommand(context.Background(), startCommand("before-fresh-reading")); err == nil || err.Code != "device_offline" {
		t.Fatalf("retained reading authorized actuation: %v", err)
	}
	messages := make(chan mqttPublication, 4)
	observer := connectTestMQTT(t, external.config.MQTTURL, "post-reconnect-observer", messages)
	waitToken(t, observer.Subscribe("mixer/mixer-01/command", 1, nil))
	telemetry.Seq++
	payload, _ = json.Marshal(telemetry)
	waitToken(t, observer.Publish("mixer/mixer-01/telemetry", 1, false, payload))
	eventually(t, "fresh reading after reconnect", func() bool { return external.snapshot().Devices[0].Online })
	if _, err := external.publishCommand(context.Background(), startCommand("after-reconnect")); err != nil {
		t.Fatal(err)
	}
	publication := nextMQTT(t, messages, "mixer/mixer-01/command")
	var cmd Command
	_ = json.Unmarshal(publication.payload, &cmd)
	if cmd.ID != "after-reconnect" || publication.retained {
		t.Fatalf("old command resumed after reconnect: %s", publication.payload)
	}
	select {
	case unexpected := <-messages:
		t.Fatalf("extra command after reconnect: %s", unexpected.payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHTTPValidationAndApplicationOrigin(t *testing.T) {
	s, _ := readyService(t)
	server := httptest.NewServer(s.handler(nil))
	defer server.Close()
	cases := []struct {
		method, path, contentType, body string
		status                          int
	}{
		{"POST", "/api/state", "", "", 405},
		{"GET", "/api/command", "", "", 405},
		{"POST", "/api/command", "text/plain", `{}`, 415},
		{"POST", "/api/command", "application/json", `{`, 400},
		{"POST", "/api/command", "application/json", `{} {}`, 400},
		{"POST", "/api/command", "application/json", `{"unknown":true}`, 400},
		{"POST", "/api/command", "application/json", strings.Repeat(" ", 16*1024) + `{}`, 400},
		{"GET", "/api/missing", "", "", 404},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d %s %s", i, tc.method, tc.path), func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("HTTP status = %d, wanted %d", response.StatusCode, tc.status)
			}
		})
	}
	header := http.Header{"Origin": []string{"https://other.example"}}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
	if conn != nil {
		conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin application WebSocket was accepted")
	}
	if response != nil {
		response.Body.Close()
	}
}
