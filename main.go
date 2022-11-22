package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	"github.com/brutella/hc/characteristic"
	hclog "github.com/brutella/hc/log"
	"github.com/brutella/hc/service"

	"github.com/joeshaw/myq"
)

type duration time.Duration

func (d duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmt.Sprintf("%s", time.Duration(d)))
}

func (d *duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = duration(time.Duration(value))
		return nil
	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = duration(tmp)
		return nil
	default:
		return errors.New("invalid duration")
	}
}

type Config struct {
	// Storage path for information about the HomeKit accessory.
	// Defaults to ~/.homecontrol
	StoragePath string `json:"storage_path"`

	// MyQ username (email address)
	Username string `json:"username"`

	// MyQ password
	Password string `json:"password"`

	// MyQ device serial number
	SerialNumber string `json:"serial_number"`

	// Accessory name.  Defaults to "Garage Door"
	AccessoryName string `json:"accessory_name"`

	// HomeKit PIN.  Defaults to 00102003.
	HomekitPIN string `json:"homekit_pin"`

	// Update interval.  Defaults to 5m
	UpdateInterval duration `json:"update_interval"`
}

func main() {
	var configFile string

	flag.StringVar(&configFile, "config", "config.json", "config file")
	flag.Parse()

	// Default values
	config := Config{
		StoragePath:    filepath.Join(os.Getenv("HOME"), ".homecontrol", "myq"),
		AccessoryName:  "Garage Door",
		HomekitPIN:     "00102003",
		UpdateInterval: duration(1 * time.Minute),
	}

	f, err := os.Open(configFile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if err := dec.Decode(&config); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if x := os.Getenv("HC_DEBUG"); x != "" {
		hclog.Debug.Enable()
	}

	s := &myq.Session{
		Username: config.Username,
		Password: config.Password,
	}

	log.Println("Connecting to MyQ service")
	if err := s.Login(); err != nil {
		log.Fatal(err)
	}
	log.Println("Connected to MyQ")

	devices, err := s.Devices()
	if err != nil {
		log.Fatal(err)
	}

	var device myq.Device
	for _, d := range devices {
		if d.SerialNumber == config.SerialNumber {
			device = d
			break
		}
	}

	if device.SerialNumber == "" {
		log.Fatalf("couldn't find device %q", config.SerialNumber)
	}

	info := accessory.Info{
		Name:         config.AccessoryName,
		SerialNumber: device.SerialNumber,
	}

	acc := accessory.New(info, accessory.TypeGarageDoorOpener)
	svc := service.NewGarageDoorOpener()
	acc.AddService(svc.Service)

	updateCurrentState := func() (string, error) {
		state, err := s.DeviceState(device.SerialNumber)
		if err != nil {
			return "", err
		}

		i := svc.CurrentDoorState.Int

		switch state {
		case myq.StateOpen:
			i.SetValue(characteristic.CurrentDoorStateOpen)
		case myq.StateClosed:
			i.SetValue(characteristic.CurrentDoorStateClosed)
		case myq.StateStopped:
			i.SetValue(characteristic.CurrentDoorStateStopped)
		}

		log.Printf("Door state is %s", state)

		return state, nil
	}

	setTargetState := func(state string) {
		t := svc.TargetDoorState.Int
		switch state {
		case myq.StateOpen:
			t.SetValue(characteristic.TargetDoorStateOpen)
		case myq.StateClosed:
			t.SetValue(characteristic.TargetDoorStateClosed)
		}
	}

	desiredCh := make(chan string, 1)

	svc.TargetDoorState.OnValueRemoteUpdate(func(st int) {
		var action, desiredState string
		switch st {
		case characteristic.TargetDoorStateOpen:
			action = myq.ActionOpen
			desiredState = myq.StateOpen
		case characteristic.TargetDoorStateClosed:
			action = myq.ActionClose
			desiredState = myq.StateClosed
		}

		log.Printf("Setting garage door to %s", action)
		if err := s.SetDoorState(device.SerialNumber, action); err != nil {
			log.Printf("Unable to set garage door state: %v", err)
			return
		}

		desiredCh <- desiredState
	})

	hcConfig := hc.Config{
		Pin:         config.HomekitPIN,
		StoragePath: filepath.Join(config.StoragePath, info.Name),
	}
	t, err := hc.NewIPTransport(hcConfig, acc)
	if err != nil {
		log.Fatal(err)
	}

	hc.OnTermination(func() {
		cancel()
		<-t.Stop()
	})

	go func() {
		log.Println("Entering garage door state update loop")
		defer log.Println("Exiting garage door state update loop")

		t := time.NewTicker(time.Duration(config.UpdateInterval))
		defer t.Stop()

		updateAndResetTarget := func() {
			state, err := updateCurrentState()
			if err != nil {
				log.Printf("Unable to update current state: %v", err)
				return
			}
			setTargetState(state)
		}

		// Set initial state
		updateAndResetTarget()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				updateAndResetTarget()
			case desiredState := <-desiredCh:
				// On a state change, update more often than normal
				start := time.Now()
				deadline := start.Add(90 * time.Second)
				for time.Now().Before(deadline) {
					time.Sleep(5 * time.Second)
					state, _ := updateCurrentState()
					if state == desiredState {
						setTargetState(state)
						log.Printf("Door reached target state (%s) after %v", desiredState, time.Since(start))
						break
					}
				}
			}
		}
	}()

	log.Println("Starting transport...")
	t.Start()
}
