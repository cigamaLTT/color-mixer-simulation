package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
)

func (s *Service) handler(web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "broker_connected": s.snapshot().Broker.Connected})
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, s.snapshot())
	})
	mux.HandleFunc("/api/command", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			writeAPIError(w, apiError(http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json", ""))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		defer r.Body.Close()
		var req CommandRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
			writeAPIError(w, apiError(http.StatusBadRequest, "invalid_json", "Expected one valid JSON command object", ""))
			return
		}
		result, apiErr := s.publishCommand(r.Context(), req)
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})
	mux.HandleFunc("/ws", s.handleSocket)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, apiError(http.StatusNotFound, "not_found", "Unknown API endpoint", ""))
	})
	if web != nil {
		mux.Handle("/", http.FileServer(http.FS(web)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.URL.Path == "/api/state" || r.URL.Path == "/healthz" {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeAPIError(w, apiError(http.StatusMethodNotAllowed, "method_not_allowed", "Use "+method, ""))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, e *APIError) {
	writeJSON(w, e.status, map[string]any{"error": e, "id": e.ID})
}

func isNormalHTTPShutdown(err error) bool {
	return err == nil || errors.Is(err, http.ErrServerClosed)
}
