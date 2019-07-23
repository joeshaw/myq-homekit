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

	// MyQ brand.  Defaults to "liftmaster"
	Brand string `json:"brand"`

	// MyQ device ID
	DeviceID string `json:"device_id"`

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
		Brand:          "liftmaster",
		AccessoryName:  "Garage Door",
		HomekitPIN:     "00102003",
		UpdateInterval: duration(5 * time.Minute),
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
		Brand:    config.Brand,
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
		if d.DeviceID == config.DeviceID {
			device = d
			break
		}
	}

	if device.DeviceID == "" {
		log.Fatalf("couldn't find device ID %q", config.DeviceID)
	}

	info := accessory.Info{
		Name:         config.AccessoryName,
		Manufacturer: config.Brand,
		Model:        device.Desc,
		SerialNumber: device.SerialNumber,
	}

	acc := accessory.New(info, accessory.TypeGarageDoorOpener)
	svc := service.NewGarageDoorOpener()
	acc.AddService(svc.Service)

	updateCurrentState := func() (string, error) {
		state, err := s.DeviceState(device.DeviceID)
		if err != nil {
			return "", err
		}

		i := svc.CurrentDoorState.Int
		t := svc.TargetDoorState.Int

		switch state {
		case myq.StateOpen:
			i.SetValue(characteristic.CurrentDoorStateOpen)
			t.SetValue(characteristic.TargetDoorStateOpen)
		case myq.StateClosed:
			i.SetValue(characteristic.CurrentDoorStateClosed)
			t.SetValue(characteristic.TargetDoorStateClosed)
		case myq.StateOpening:
			i.SetValue(characteristic.CurrentDoorStateOpening)
			t.SetValue(characteristic.TargetDoorStateOpen)
		case myq.StateClosing:
			i.SetValue(characteristic.CurrentDoorStateClosing)
			t.SetValue(characteristic.TargetDoorStateClosed)
		case myq.StateStopped:
			i.SetValue(characteristic.CurrentDoorStateStopped)
		}

		log.Printf("Door state is %s", state)

		return state, nil
	}

	svc.TargetDoorState.OnValueRemoteUpdate(func(st int) {
		var desiredState string
		switch st {
		case characteristic.TargetDoorStateOpen:
			desiredState = myq.StateOpen
		case characteristic.TargetDoorStateClosed:
			desiredState = myq.StateClosed
		}

		log.Printf("Setting garage door to %s", desiredState)
		if err := s.SetDeviceState(device.DeviceID, desiredState); err != nil {
			log.Printf("Unable to set garage door state: %v", err)
			return
		}

		// Update the current state more often than the normal
		// status loop.  It has to run in a goroutine because
		// this update function can't block.  There's an initial
		// delay because the API will often report back the old
		// state, not the state we're moving to.
		go func() {
			start := time.Now()
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(5 * time.Second)
				state, _ := updateCurrentState()
				if state == desiredState {
					log.Printf("Door reached target state (%s) after %v", desiredState, time.Since(start))
					break
				}
			}
		}()
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

		var ch <-chan time.Time

		updateState := func() {
			state, err := updateCurrentState()
			if err != nil {
				log.Printf("Error fetching current state: %v", err)
			}
			// If the door is in a transitional state, check much more
			// often.
			if state == myq.StateOpening || state == myq.StateClosing {
				ch = time.After(5 * time.Second)
			} else {
				ch = nil
			}
		}

		// Set initial state
		updateState()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				updateState()
			case <-ch:
				updateState()
			}
		}

	}()

	log.Println("Starting transport...")
	t.Start()
}
