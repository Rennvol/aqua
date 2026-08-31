package main

import (
	"log"
	"math"
	"time"
)

// RunMock simulates sensor data when no UNO is connected.
// Temperatures drifts around 28°C, voltage ~12V, current varies.
func RunMock() {
	state.mu.Lock()
	state.Mode = "mock"
	state.Connected = false
	state.mu.Unlock()

	t := 28.0
	v := 12.0
	i := 0.0
	step := 0.0
	lampOn := false
	fanOn := false

	for {
		step += 0.3
		// simulate temperature: 28±0.5°C with slow drift
		t = 28 + 0.5*math.Sin(step*0.1)
		// simulate voltage: 12.0±0.1V
		v = 12.0 + 0.1*math.Sin(step*0.05)
		// simulate current: varies 0.3-1.5A depending on loads
		base := 0.0
		if lampOn {
			base += 0.8
		}
		if fanOn {
			base += 0.3
		}
		i = base + 0.2*math.Sin(step*0.2)

		state.setSensor(t, v, math.Abs(i))

		// read relay state from the shared state
		state.mu.RLock()
		lampOn = state.RelayLamp
		fanOn = state.RelayFan
		state.mu.RUnlock()

		time.Sleep(2 * time.Second)
	}
}

// RunSerial reads from serial port.
// For now, just a stub — will be implemented when hardware arrives.
func RunSerial() {
	state.mu.Lock()
	state.Mode = "serial"
	state.mu.Unlock()

	log.Println("serial: waiting for /dev/ttyACM0...")
	// TODO: open /dev/ttyACM0 (or /dev/ttyUSB0), read JSON lines, parse into state
	// For now, fall back to mock
	log.Println("serial: no device found, falling back to mock")
	RunMock()
}