// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

// ConnectRelay registers with a relay server and bridges traffic.
// It connects to relayURL/register, receives an instance ID, and
// then bidirectionally forwards messages between the relay and the
// server's remote client handler.
//
// Returns the instance ID and a channel that closes when the relay
// connection drops.
func (s *Server) ConnectRelay(ctx context.Context, relayURL string) (string, error) {
	registerURL := relayURL + "/register"
	slog.Info("connecting to relay", "url", registerURL)

	conn, _, err := websocket.Dial(ctx, registerURL, nil)
	if err != nil {
		return "", err
	}

	// Read the instance ID sent by the relay.
	_, idBytes, err := conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		return "", err
	}
	instanceID := string(idBytes)
	slog.Info("registered with relay", "instance_id", instanceID)

	// Register as a virtual remote client.
	rc := remoteConn{conn: conn, ctx: ctx}
	s.mu.Lock()
	s.remotes[conn] = rc
	s.mu.Unlock()

	// Send init + history + scripts to the relay connection (same
	// as handleRemote does for direct clients).
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()

	if len(hist) > 0 {
		s.writeJSON(conn, ctx, map[string]any{
			"type":    "history",
			"entries": hist,
		})
	}

	if s.luaRT != nil {
		if source, err := s.luaRT.Scripts(); err != nil {
			slog.Error("relay: failed to read lua scripts", "err", err)
		} else if source != "" {
			s.writeJSON(conn, ctx, map[string]any{
				"type":   "scripts",
				"source": source,
			})
		}
	}

	// Read loop: process messages from the relay (i.e., from the iOS
	// client on the other side). Runs in a goroutine.
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.remotes, conn)
			s.mu.Unlock()
			conn.CloseNow()
			slog.Info("relay connection closed", "instance_id", instanceID)
		}()

		for {
			mt, data, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("relay read error", "err", err)
				}
				return
			}

			if mt == websocket.MessageBinary {
				// sqlpipe sync frames — handle the same way as handleRemote.
				if s.syncMgr != nil {
					if resp, err := s.syncMgr.HandleMessage(data); err != nil {
						slog.Error("relay: sync receive failed", "err", err)
					} else if len(resp) > 0 {
						writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
						conn.Write(writeCtx, websocket.MessageBinary, resp)
						cancel()
					}
				}
				continue
			}

			// JSON text message — parse action/send_message.
			var msg struct {
				Type   string `json:"type"`
				Action string `json:"action"`
				Value  string `json:"value"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				slog.Warn("relay: invalid JSON", "err", err)
				continue
			}

			switch msg.Type {
			case "action":
				s.HandleAction(msg.Action, msg.Value)
			case "user_message":
				s.HandleUserMessage(msg.Text)
			case "control":
				s.handleControl(conn, ctx, msg.Action, msg.Value)
			}
		}
	}()

	return instanceID, nil
}
