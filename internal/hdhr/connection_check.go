package hdhr

import (
	"context"
	"errors"
	"fmt"
)

// CheckConfiguration discovers the staged devices and reads their lineups.
// It never configures an engine, reads guide data, or reserves a tuner.
//
// The Devices editor sends only the endpoint being edited, so saved tuners
// cannot fail its check. When a caller stages several enabled endpoints, a
// failure names the endpoint by position.
func CheckConfiguration(ctx context.Context, cfg Config) error {
	cfg, err := cfg.Normalize()
	if err != nil {
		return err
	}
	enabled := 0
	for _, endpoint := range cfg.Endpoints {
		if endpoint.IsEnabled() {
			enabled++
		}
	}
	if enabled == 0 {
		return errors.New("Enable the endpoint before checking its connection.")
	}
	for i, endpoint := range cfg.Endpoints {
		if !endpoint.IsEnabled() {
			continue
		}
		if err := checkEndpoint(ctx, endpoint); err != nil {
			// URLs and upstream bodies may contain credentials. Never echo
			// the endpoint's address or label.
			if enabled == 1 {
				return err
			}
			return fmt.Errorf("Endpoint %d: %w", i+1, err)
		}
	}
	return nil
}

// Connection check messages are complete sentences shown as-is in the editor.
var (
	errCheckAddress   = errors.New("This address can't be used. Check the device address and additional stream hosts.")
	errCheckDiscovery = errors.New("The device didn't answer. Check the address and that the Silo server can reach it on your network.")
	errCheckLineup    = errors.New("The device answered, but its channel lineup couldn't be read. Check the address and additional stream hosts.")
	errCheckChannels  = errors.New("The device has no channels. Run a channel scan on the tuner, then check again.")
)

func checkEndpoint(ctx context.Context, endpoint Endpoint) error {
	client, err := NewDeviceClient(ctx, endpoint)
	if err != nil {
		return errCheckAddress
	}
	defer client.Close()
	device, err := client.Discover(ctx)
	if err != nil {
		return errCheckDiscovery
	}
	lineup, err := client.Lineup(ctx, device)
	if err != nil {
		return errCheckLineup
	}
	if len(lineup) == 0 {
		return errCheckChannels
	}
	return nil
}
