package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config keeps the lab's network listeners local unless explicitly overridden.
type Config struct {
	HTTPAddr       string
	MQTTAddr       string
	MQTTWSAddr     string
	MQTTURL        string
	MQTTUsername   string
	MQTTPassword   string
	DeviceID       string
	WebDir         string
	StaleAfter     time.Duration
	OfflineAfter   time.Duration
	PublishTimeout time.Duration
}

func defaultConfig() Config {
	return Config{
		HTTPAddr: "127.0.0.1:8080", MQTTAddr: "127.0.0.1:1883", MQTTWSAddr: "127.0.0.1:9001",
		DeviceID: "mixer-01", WebDir: "web", StaleAfter: 5 * time.Second,
		OfflineAfter: 15 * time.Second, PublishTimeout: 3 * time.Second,
	}
}

func loadConfig() (Config, error) {
	c := defaultConfig()
	for name, dest := range map[string]*string{
		"HTTP_ADDR": &c.HTTPAddr, "MQTT_ADDR": &c.MQTTAddr, "MQTT_WS_ADDR": &c.MQTTWSAddr,
		"MQTT_URL": &c.MQTTURL, "MQTT_USERNAME": &c.MQTTUsername, "MQTT_PASSWORD": &c.MQTTPassword,
		"DEVICE_ID": &c.DeviceID, "WEB_DIR": &c.WebDir,
	} {
		if v, ok := os.LookupEnv(name); ok {
			*dest = v
		}
	}
	if _, explicit := os.LookupEnv("WEB_DIR"); !explicit {
		// Support both `go run .` from iot/ and the project launch scripts.
		for _, candidate := range []string{"web", filepath.Join("iot", "web")} {
			if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
				c.WebDir = candidate
				break
			}
		}
	}
	for name, dest := range map[string]*time.Duration{
		"STALE_AFTER": &c.StaleAfter, "OFFLINE_AFTER": &c.OfflineAfter,
	} {
		if v := os.Getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return c, fmt.Errorf("%s must be a positive duration", name)
			}
			*dest = d
		}
	}
	if !validDeviceID(c.DeviceID) {
		return c, fmt.Errorf("DEVICE_ID must contain 1–64 letters, digits, underscores or hyphens")
	}
	if c.OfflineAfter < c.StaleAfter {
		return c, fmt.Errorf("OFFLINE_AFTER must be at least STALE_AFTER")
	}
	if c.MQTTURL != "" {
		u, err := url.Parse(c.MQTTURL)
		if err != nil || u.Host == "" {
			return c, fmt.Errorf("MQTT_URL must be a valid MQTT broker URL")
		}
		if u.Scheme == "mqtt" {
			u.Scheme = "tcp"
		}
		if u.Scheme == "mqtts" {
			u.Scheme = "ssl"
		}
		switch u.Scheme {
		case "tcp", "ssl", "tls", "ws", "wss":
		default:
			return c, fmt.Errorf("MQTT_URL scheme must be tcp, ssl, ws or wss")
		}
		c.MQTTURL = u.String()
	}
	return c, nil
}

func publicBrokerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimSuffix(u.String(), "/")
}
