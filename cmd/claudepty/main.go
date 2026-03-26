// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// claudepty spawns a persistent Claude Code instance and exposes it
// over a WebSocket with an embedded web UI.
//
// Usage: claudepty [--port 9119] [--workdir .]
package main

import (
	_ "embed"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/jevon/internal/claude"
)

//go:embed index.html
var indexHTML []byte

type server struct {
	proc      *claude.Process
	mu        sync.Mutex
	listeners []chan string
}

func main() {
	port := "9119"
	workdir := "."

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			port = args[i]
		case "--workdir":
			i++
			workdir = args[i]
		default:
			workdir = args[i]
		}
	}

	proc, err := claude.Start(claude.Config{WorkDir: workdir})
	if err != nil {
		slog.Error("start failed", "err", err)
		os.Exit(1)
	}
	slog.Info("started", "session", proc.SessionID(), "jsonl", proc.JSONLPath())

	s := &server{proc: proc}

	proc.OnEvent(func(ev claude.Event) {
		prettyPrint(string(ev.Raw))
		s.broadcast(string(ev.Raw))
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveIndex)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /stop", s.handleStop)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down")
		proc.Stop()
		os.Exit(0)
	}()

	addr := ":" + port
	slog.Info("listening", "addr", addr, "ui", "http://localhost:"+port)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "err", err)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.Error("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	slog.Info("client connected")

	ch := make(chan string, 256)
	s.mu.Lock()
	s.listeners = append(s.listeners, ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		for i, l := range s.listeners {
			if l == ch {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		slog.Info("client disconnected")
	}()

	// Server → Client.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-ch:
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := conn.Write(writeCtx, websocket.MessageText, []byte(line)); err != nil {
					cancel()
					return
				}
				cancel()
			}
		}
	}()

	// Client → Server.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			continue
		}
		slog.Info("received", "msg", msg)
		if err := s.proc.Send(msg); err != nil {
			slog.Error("send failed", "err", err)
		}
	}
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"alive":     s.proc.Alive(),
		"sessionID": s.proc.SessionID(),
		"jsonl":     s.proc.JSONLPath(),
	})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.proc.Stop()
	fmt.Fprintf(w, "stopping\n")
}

func (s *server) broadcast(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.listeners {
		select {
		case ch <- line:
		default:
		}
	}
}

// MARK: - Pretty printing

const (
	cReset  = "\033[0m"
	cKey    = "\033[38;5;117m"
	cString = "\033[38;5;179m"
	cNumber = "\033[38;5;150m"
	cBool   = "\033[38;5;204m"
	cNull   = "\033[38;5;243m"
	cBrace  = "\033[38;5;243m"
)

func prettyPrint(line string) {
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		fmt.Println(line)
		return
	}
	printValue(v)
	fmt.Println()
}

func printValue(v any) {
	switch val := v.(type) {
	case map[string]any:
		fmt.Printf("%s{%s", cBrace, cReset)
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		for i, k := range keys {
			fmt.Printf("%s%s%s: ", cKey, json5Key(k), cReset)
			printValue(val[k])
			if i < len(keys)-1 {
				fmt.Print(", ")
			}
		}
		fmt.Printf("%s}%s", cBrace, cReset)
	case []any:
		fmt.Printf("%s[%s", cBrace, cReset)
		for i, item := range val {
			printValue(item)
			if i < len(val)-1 {
				fmt.Print(", ")
			}
		}
		fmt.Printf("%s]%s", cBrace, cReset)
	case string:
		s := val
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		fmt.Printf("%s%q%s", cString, s, cReset)
	case float64:
		if val == float64(int64(val)) {
			fmt.Printf("%s%d%s", cNumber, int64(val), cReset)
		} else {
			fmt.Printf("%s%g%s", cNumber, val, cReset)
		}
	case bool:
		fmt.Printf("%s%t%s", cBool, val, cReset)
	case nil:
		fmt.Printf("%snull%s", cNull, cReset)
	default:
		fmt.Printf("%v", val)
	}
}

func json5Key(k string) string {
	if len(k) == 0 {
		return fmt.Sprintf("%q", k)
	}
	for i, r := range k {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '$') {
				return fmt.Sprintf("%q", k)
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '$') {
				return fmt.Sprintf("%q", k)
			}
		}
	}
	return k
}
