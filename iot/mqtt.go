package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

type embeddedBroker struct {
	server    *mqtt.Server
	app       *Service
	status    BrokerStatus
	closeOnce sync.Once
	closeErr  error
}

func startEmbeddedBroker(app *Service) (*embeddedBroker, error) {
	server := mqtt.New(&mqtt.Options{InlineClient: true, Logger: app.log})
	server.Options.Capabilities.MaximumPacketSize = 64 * 1024
	server.Options.Capabilities.MaximumClientWritesPending = 128
	server.Options.Capabilities.MaximumInflight = 128
	server.Options.Capabilities.MaximumSessionExpiryInterval = 0
	if err := server.AddHook(&deviceHook{app: app}, nil); err != nil {
		return nil, err
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "mqtt-tcp", Address: app.config.MQTTAddr})
	if err := server.AddListener(tcp); err != nil {
		return nil, fmt.Errorf("MQTT TCP listener: %w", err)
	}
	ws := &mqttWebsocketListener{address: app.config.MQTTWSAddr}
	if err := server.AddListener(ws); err != nil {
		tcp.Close(func(string) {})
		return nil, fmt.Errorf("MQTT WebSocket listener: %w", err)
	}
	if err := server.Serve(); err != nil {
		_ = server.Close()
		return nil, err
	}
	b := &embeddedBroker{server: server, app: app, status: BrokerStatus{
		Mode: "embedded", Connected: true, TCPAddress: tcp.Address(), WSAddress: ws.Address(),
	}}
	app.setPublisher(b)
	app.setBroker(b.status)
	return b, nil
}

func (b *embeddedBroker) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// No retained actuator messages or persistent sessions are accepted by this broker.
	return b.server.Publish(topic, payload, false, 1)
}

func (b *embeddedBroker) Close() error {
	b.closeOnce.Do(func() {
		status := b.status
		status.Connected = false
		b.app.setBroker(status)
		b.closeErr = b.server.Close()
	})
	return b.closeErr
}

type deviceHook struct {
	mqtt.HookBase
	app *Service
}

func (h *deviceHook) ID() string { return "mixer-lab" }

func (h *deviceHook) Provides(b byte) bool {
	switch b {
	case mqtt.OnConnectAuthenticate, mqtt.OnACLCheck, mqtt.OnPublish, mqtt.OnPublished, mqtt.OnWillSent:
		return true
	}
	return false
}

func (h *deviceHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	// Persistent MQTT sessions can replay an actuator command after reconnect.
	// This lab deliberately requires clean sessions on every connection.
	// A Last Will is dispatched outside OnPublish, so it cannot be a command.
	return pk.Connect.Clean && !(pk.Connect.WillFlag && strings.HasSuffix(pk.Connect.WillTopic, "/command"))
}

func (h *deviceHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	return write || !strings.HasSuffix(topic, "/command") || !cl.Closed()
}

func (h *deviceHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if strings.HasSuffix(pk.TopicName, "/command") && pk.FixedHeader.Retain {
		return pk, packets.ErrRejectPacket
	}
	return pk, nil
}

func (h *deviceHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	// This hook observes live publications, rather than retained subscription replay.
	h.app.ingest(pk.TopicName, pk.Payload, false)
}

func (h *deviceHook) OnWillSent(cl *mqtt.Client, pk packets.Packet) {
	h.app.ingest(pk.TopicName, pk.Payload, false)
}

// Bind before Serve so a busy MQTT-over-WebSocket port is a startup error.
// Mochi's packaged WebSocket listener binds asynchronously and cannot report it.
type mqttWebsocketListener struct {
	address  string
	listener net.Listener
	server   *http.Server
	log      *slog.Logger
	once     sync.Once
}

func (l *mqttWebsocketListener) ID() string       { return "mqtt-websocket" }
func (l *mqttWebsocketListener) Protocol() string { return "ws" }
func (l *mqttWebsocketListener) Address() string {
	if l.listener != nil {
		return l.listener.Addr().String()
	}
	return l.address
}

func (l *mqttWebsocketListener) Init(logger *slog.Logger) error {
	l.log = logger
	var err error
	l.listener, err = net.Listen("tcp", l.address)
	return err
}

func (l *mqttWebsocketListener) Serve(establish listeners.EstablishFn) {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mqtt"},
		// MQTT is the lab's anonymous broker protocol. The dashboard itself uses
		// the origin-checked application WebSocket on the HTTP listener.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	l.server = &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetReadLimit(64 * 1024)
			if err := establish(l.ID(), &mqttWebsocketConn{Conn: conn.UnderlyingConn(), socket: conn}); err != nil {
				l.log.Debug("MQTT WebSocket disconnected", "error", err)
			}
		}),
	}
	if err := l.server.Serve(l.listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		l.log.Error("MQTT WebSocket listener stopped", "error", err)
	}
}

func (l *mqttWebsocketListener) Close(closeClients listeners.CloseFn) {
	l.once.Do(func() {
		_ = l.listener.Close()
		closeClients(l.ID())
	})
}

type mqttWebsocketConn struct {
	net.Conn
	socket  *websocket.Conn
	reader  io.Reader
	writeMu sync.Mutex
}

func (c *mqttWebsocketConn) Read(p []byte) (int, error) {
	for {
		if c.reader == nil {
			kind, reader, err := c.socket.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("MQTT WebSocket messages must be binary")
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *mqttWebsocketConn) Write(p []byte) (int, error) {
	// net.Conn permits concurrent writes; Gorilla requires one data writer.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.socket.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *mqttWebsocketConn) Close() error { return c.socket.Close() }

type externalBroker struct {
	app    *Service
	mu     sync.RWMutex
	client paho.Client
	done   chan struct{}
}

func startExternalBroker(ctx context.Context, app *Service) *externalBroker {
	b := &externalBroker{app: app, done: make(chan struct{})}
	app.setPublisher(b)
	go b.run(ctx)
	return b
}

func (b *externalBroker) status(connected bool) BrokerStatus {
	return BrokerStatus{Mode: "external", Connected: connected, ExternalURL: publicBrokerURL(b.app.config.MQTTURL)}
}

func (b *externalBroker) run(ctx context.Context) {
	defer close(b.done)
	defer b.app.setBroker(b.status(false))
	for ctx.Err() == nil {
		lost := make(chan struct{}, 1)
		opts := paho.NewClientOptions().AddBroker(b.app.config.MQTTURL).
			SetClientID(fmt.Sprintf("mixer-web-%d", time.Now().UnixNano())).
			SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false).
			SetConnectTimeout(3 * time.Second).SetWriteTimeout(2 * time.Second).
			SetKeepAlive(10 * time.Second).SetPingTimeout(3 * time.Second).
			SetOrderMatters(true).
			SetUsername(b.app.config.MQTTUsername).SetPassword(b.app.config.MQTTPassword)
		opts.SetConnectionLostHandler(func(_ paho.Client, _ error) {
			b.app.setBroker(b.status(false))
			select {
			case lost <- struct{}{}:
			default:
			}
		})
		opts.SetDefaultPublishHandler(func(_ paho.Client, msg paho.Message) {
			b.app.ingest(msg.Topic(), msg.Payload(), msg.Retained())
		})
		client := paho.NewClient(opts)
		connectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := waitMQTTToken(connectCtx, client.Connect())
		cancel()
		if err == nil {
			subCtx, subCancel := context.WithTimeout(ctx, 3*time.Second)
			err = waitMQTTToken(subCtx, client.SubscribeMultiple(map[string]byte{
				"mixer/+/telemetry": 0, "mixer/+/status": 1, "mixer/+/event": 1,
			}, nil))
			subCancel()
		}
		if err == nil && client.IsConnectionOpen() && ctx.Err() == nil {
			b.mu.Lock()
			b.client = client
			b.mu.Unlock()
			b.app.setBroker(b.status(true))
			select {
			case <-ctx.Done():
			case <-lost:
			}
		} else if ctx.Err() == nil {
			b.app.log.Warn("external MQTT unavailable; retrying", "broker", publicBrokerURL(b.app.config.MQTTURL))
		}
		b.mu.Lock()
		b.client = nil
		b.mu.Unlock()
		client.Disconnect(0)
		b.app.setBroker(b.status(false))
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *externalBroker) Publish(ctx context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()
	if client == nil || !client.IsConnectionOpen() {
		return errors.New("broker disconnected")
	}
	// Auto-reconnect is disabled on this client. A fresh clean-session client is
	// created by run, so interrupted actuator messages are never resumed later.
	return waitMQTTToken(ctx, client.Publish(topic, 1, false, payload))
}

func waitMQTTToken(ctx context.Context, token paho.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

func (b *externalBroker) Close() error {
	<-b.done // The owner's context is canceled before Close.
	return nil
}
