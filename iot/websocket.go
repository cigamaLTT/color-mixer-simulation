package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type socketHub struct {
	mu      sync.Mutex
	clients map[*socketClient]struct{}
}

type socketClient struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func newSocketHub() *socketHub { return &socketHub{clients: make(map[*socketClient]struct{})} }

func (h *socketHub) add(c *socketClient) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= 128 {
		return false
	}
	h.clients[c] = struct{}{}
	return true
}

func (h *socketHub) remove(c *socketClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.close()
}

func (c *socketClient) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (h *socketHub) broadcast(msg Message) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case <-client.done:
			delete(h.clients, client)
		case client.send <- payload:
		default:
			delete(h.clients, client)
			client.close()
		}
	}
}

func (h *socketHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		client.close()
		delete(h.clients, client)
	}
}

func (c *socketClient) enqueue(msg Message) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- payload:
	case <-c.done:
	default:
		c.close()
	}
}

func (s *Service) handleSocket(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	// Gorilla's default origin policy accepts only same-host browser connections.
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 4096}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &socketClient{conn: conn, send: make(chan []byte, 64), done: make(chan struct{})}
	client.enqueue(Message{Type: "state", Data: s.snapshot()})
	if !s.hub.add(client) {
		client.close()
		return
	}
	defer s.hub.remove(client)
	go client.writeLoop()
	conn.SetReadLimit(16 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	for {
		kind, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.TextMessage {
			client.enqueue(Message{Type: "error", Data: APIError{Code: "invalid_message", Message: "Commands must be JSON text messages"}})
			continue
		}
		var req CommandRequest
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF || req.Type != "command" {
			client.enqueue(Message{Type: "error", Data: APIError{Code: "invalid_json", Message: "Expected a command envelope with type command"}})
			continue
		}
		result, apiErr := s.publishCommand(r.Context(), req)
		if apiErr != nil {
			client.enqueue(Message{Type: "error", DeviceID: req.DeviceID, Data: apiErr})
		} else if result.Duplicate {
			client.enqueue(Message{Type: "command_ack", DeviceID: result.DeviceID, Data: result})
		}
	}
}

func (c *socketClient) writeLoop() {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	defer c.close()
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if c.conn.WriteMessage(websocket.TextMessage, payload) != nil {
				return
			}
		case <-ping.C:
			if c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
