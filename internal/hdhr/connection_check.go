package hdhr

import (
	"context"
	"errors"
	"fmt"
)

// CheckConfiguration discovers the staged devices and reads their lineups.
// It never configures an engine, reads guide data, or reserves a tuner.
func CheckConfiguration(ctx context.Context, cfg Config) error {
	cfg, err := cfg.Normalize()
	if err != nil {
		return err
	}
	checked := 0
	for i, endpoint := range cfg.Endpoints {
		if !endpoint.IsEnabled() {
			continue
		}
		if err := checkEndpoint(ctx, endpoint); err != nil {
			// URLs and upstream bodies may contain credentials. Refer to the
			// endpoint's position rather than echoing its address or label.
			return fmt.Errorf("endpoint %d: %w", i+1, err)
		}
		checked++
	}
	if checked == 0 {
		return errors.New("add and enable at least one endpoint before checking the connection")
	}
	return nil
}

func checkEndpoint(ctx context.Context, endpoint Endpoint) error {
	client, err := NewDeviceClient(ctx, endpoint)
	if err != nil {
		return errors.New("check the device address and allowed hosts")
	}
	defer client.Close()
	device, err := client.Discover(ctx)
	if err != nil {
		return errors.New("discovery failed; check the address and whether the device is reachable")
	}
	lineup, err := client.Lineup(ctx, device)
	if err != nil {
		return errors.New("the device lineup could not be read; check its address and allowed hosts")
	}
	if len(lineup) == 0 {
		return errors.New("the device has no channels; scan or configure its lineup first")
	}
	return nil
}
