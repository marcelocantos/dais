// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package qr generates QR codes for jevond server discovery.
package qr

import (
	"fmt"
	"io"
	"net"

	"github.com/skip2/go-qrcode"
)

// ServerURL returns the jevon:// URL for the given port, using the
// machine's LAN IP address.
func ServerURL(port int) (string, error) {
	ip, err := lanIP()
	if err != nil {
		return "", fmt.Errorf("detect LAN IP: %w", err)
	}
	return fmt.Sprintf("jevon://%s:%d", ip, port), nil
}

// Print writes a QR code encoding url to w using Unicode half-block
// characters.
func Print(w io.Writer, url string) {
	qr, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		fmt.Fprintf(w, "  %s\n", url)
		return
	}
	bmp := qr.Bitmap()
	rows := len(bmp)

	fmt.Fprintln(w)

	for y := 0; y < rows; y += 2 {
		fmt.Fprint(w, "  ")
		for x := 0; x < len(bmp[y]); x++ {
			top := bmp[y][x]
			bot := false
			if y+1 < rows {
				bot = bmp[y+1][x]
			}
			switch {
			case !top && !bot:
				fmt.Fprint(w, " ")
			case top && bot:
				fmt.Fprint(w, "\u2588")
			case top && !bot:
				fmt.Fprint(w, "\u2580")
			case !top && bot:
				fmt.Fprint(w, "\u2584")
			}
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "  %s\n\n", url)
}

// LanIP returns the machine's LAN IP address, or "localhost" on error.
func LanIP() string {
	ip, err := lanIP()
	if err != nil {
		return "localhost"
	}
	return ip
}

// lanIP returns the machine's LAN IP address by dialing a UDP socket
// to a public address (no packets are sent).
func lanIP() (string, error) {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), nil
}
