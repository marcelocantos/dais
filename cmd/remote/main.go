// remote is a terminal client for daisd. It connects to the shepherd
// and sends text messages, displaying streamed responses.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "daisd address")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	url := fmt.Sprintf("ws://%s/ws/remote", *addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20) // 1 MB

	// Read init message.
	_, data, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read init failed: %v\n", err)
		os.Exit(1)
	}

	var init struct {
		Version string `json:"version"`
	}
	json.Unmarshal(data, &init)
	fmt.Fprintf(os.Stderr, "Connected to daisd %s\n", init.Version)

	// Handle Ctrl-C.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nBye.")
		conn.Close(websocket.StatusNormalClosure, "user exit")
		os.Exit(0)
	}()

	// turnDone signals when the shepherd finishes a turn (status=idle).
	turnDone := make(chan struct{}, 1)

	// Response reader goroutine.
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "\nDisconnected: %v\n", err)
				}
				cancel()
				return
			}

			var msg struct {
				Type    string `json:"type"`
				Content string `json:"content,omitempty"`
				State   string `json:"state,omitempty"`
				Message string `json:"message,omitempty"`
			}
			json.Unmarshal(data, &msg)

			switch msg.Type {
			case "text":
				fmt.Print(msg.Content)
			case "status":
				if msg.State == "idle" {
					fmt.Println()
					select {
					case turnDone <- struct{}{}:
					default:
					}
				}
			case "error":
				fmt.Fprintf(os.Stderr, "\nError: %s\n", msg.Message)
				select {
				case turnDone <- struct{}{}:
				default:
				}
			}
		}
	}()

	// Detect interactive vs piped stdin.
	interactive := isTerminal(os.Stdin)

	// Read stdin lines, send as messages.
	scanner := bufio.NewScanner(os.Stdin)
	if interactive {
		fmt.Print("> ")
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if interactive {
				fmt.Print("> ")
			}
			continue
		}

		msg, _ := json.Marshal(map[string]string{
			"type": "message",
			"text": line,
		})
		if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
			fmt.Fprintf(os.Stderr, "send failed: %v\n", err)
			break
		}

		// Wait for shepherd to finish before next prompt.
		select {
		case <-turnDone:
		case <-ctx.Done():
			return
		}

		if interactive {
			fmt.Print("> ")
		}
	}

	// If stdin was piped, wait for the last turn to complete.
	if !interactive {
		select {
		case <-turnDone:
		case <-ctx.Done():
		}
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
