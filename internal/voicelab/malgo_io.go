// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package voicelab

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gen2brain/malgo"
)

// MalgoDevice owns a single full-duplex malgo device and exposes its
// capture path as an AudioSource and its playback path as an
// AudioSink. Capture frames go through a buffered channel so the
// audio thread never blocks on downstream consumers; playback uses a
// jitter buffer drained by the device callback, padded with silence on
// underrun.
type MalgoDevice struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device

	captureCh chan []byte

	pbMu  sync.Mutex
	pbBuf []byte

	closeOnce sync.Once
	closed    chan struct{}
}

// NewMalgoDevice initialises a full-duplex device at 24 kHz PCM16 mono
// (the wire format) and starts it. The caller can then attach the
// returned Source / Sink to a Loop.
func NewMalgoDevice() (*MalgoDevice, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(msg string) {
		slog.Debug("malgo", "msg", msg)
	})
	if err != nil {
		return nil, fmt.Errorf("malgo init context: %w", err)
	}

	m := &MalgoDevice{
		ctx:       ctx,
		captureCh: make(chan []byte, 64),
		closed:    make(chan struct{}),
	}

	deviceCfg := malgo.DefaultDeviceConfig(malgo.Duplex)
	deviceCfg.SampleRate = SampleRate
	deviceCfg.Capture.Format = malgo.FormatS16
	deviceCfg.Capture.Channels = 1
	deviceCfg.Playback.Format = malgo.FormatS16
	deviceCfg.Playback.Channels = 1
	deviceCfg.Alsa.NoMMap = 1

	callbacks := malgo.DeviceCallbacks{
		Data: m.onData,
	}

	device, err := malgo.InitDevice(ctx.Context, deviceCfg, callbacks)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("malgo init device: %w", err)
	}
	m.device = device

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("malgo start device: %w", err)
	}

	return m, nil
}

func (m *MalgoDevice) onData(out, in []byte, frameCount uint32) {
	if len(in) > 0 {
		buf := make([]byte, len(in))
		copy(buf, in)
		select {
		case m.captureCh <- buf:
		default:
			// Audio thread can never block; drop on backpressure.
			slog.Debug("voicelab: capture channel full, dropping frame")
		}
	}
	m.pbMu.Lock()
	n := copy(out, m.pbBuf)
	m.pbBuf = m.pbBuf[n:]
	m.pbMu.Unlock()
	for i := n; i < len(out); i++ {
		out[i] = 0
	}
}

// Source returns the AudioSource view of this device's capture stream.
func (m *MalgoDevice) Source() AudioSource { return malgoSource{m: m} }

// Sink returns the AudioSink view of this device's playback stream.
func (m *MalgoDevice) Sink() AudioSink { return malgoSink{m: m} }

// Close stops the device and frees the malgo context. After Close, the
// Source's frame channel is closed.
func (m *MalgoDevice) Close() error {
	var err error
	m.closeOnce.Do(func() {
		if m.device != nil {
			m.device.Uninit()
		}
		if m.ctx != nil {
			err = m.ctx.Uninit()
			m.ctx.Free()
		}
		close(m.captureCh)
		close(m.closed)
	})
	return err
}

type malgoSource struct{ m *MalgoDevice }

func (s malgoSource) Frames() <-chan []byte { return s.m.captureCh }
func (s malgoSource) Close() error          { return nil } // ownership lives on MalgoDevice

type malgoSink struct{ m *MalgoDevice }

func (s malgoSink) Write(pcm []byte) error {
	select {
	case <-s.m.closed:
		return errors.New("malgo device closed")
	default:
	}
	s.m.pbMu.Lock()
	s.m.pbBuf = append(s.m.pbBuf, pcm...)
	s.m.pbMu.Unlock()
	return nil
}
func (s malgoSink) Close() error { return nil }
