// Copyright 2021 Adam Chalkley
//
// https://github.com/atc0005/check-vmware
//
// Licensed under the MIT License. See LICENSE file in the project root for
// full license information.

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/atc0005/go-nagios"

	"github.com/atc0005/check-vmware/internal/config"
	"github.com/atc0005/check-vmware/internal/vsphere"

	zlog "github.com/rs/zerolog/log"
)

func main() {

	// Set initial "state" as valid, adjust as we go.
	var nagiosExitState = nagios.ExitState{
		LastError:      nil,
		ExitStatusCode: nagios.StateOKExitCode,
	}

	// defer this from the start so it is the last deferred function to run
	defer nagiosExitState.ReturnCheckResults()

	// Setup configuration by parsing user-provided flags. Note plugin type so
	// that only applicable CLI flags are exposed and any plugin-specific
	// settings are applied.
	cfg, cfgErr := config.New(config.PluginType{Tools: true})
	switch {
	case errors.Is(cfgErr, config.ErrVersionRequested):
		fmt.Println(config.Version())

		return

	case cfgErr != nil:
		// We're using the standalone Err function from rs/zerolog/log as we
		// do not have a working configuration.
		zlog.Err(cfgErr).Msg("Error initializing application")
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error initializing application",
			nagios.StateCRITICALLabel,
		)
		nagiosExitState.LastError = cfgErr
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout())
	defer cancel()

	if cfg.EmitBranding {
		// If enabled, show application details at end of notification
		nagiosExitState.BrandingCallback = config.Branding("Notification generated by ")
	}

	log := cfg.Log.With().
		Str("included_resource_pools", cfg.IncludedResourcePools.String()).
		Str("excluded_resource_pools", cfg.ExcludedResourcePools.String()).
		Str("ignored_vms", cfg.IgnoredVMs.String()).
		Bool("eval_powered_off", cfg.PoweredOff).
		Logger()

	log.Debug().Msg("Logging into vSphere environment")
	c, loginErr := vsphere.Login(
		ctx, cfg.Server, cfg.Port, cfg.TrustCert,
		cfg.Username, cfg.Domain, cfg.Password,
	)
	if loginErr != nil {
		log.Error().Err(loginErr).Msg("error logging into %s")

		nagiosExitState.LastError = loginErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error logging into %q",
			nagios.StateCRITICALLabel,
			cfg.Server,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	// At this point we're logged in, ready to retrieve a list of VMs. If
	// specified, we should limit VMs based on include/exclude lists. First,
	// we'll make sure that all specified resource pools actually exist in the
	// vSphere environment.

	log.Debug().Msg("Validating resource pools")
	validateErr := vsphere.ValidateRPs(ctx, c, cfg.IncludedResourcePools, cfg.ExcludedResourcePools)
	if validateErr != nil {
		log.Error().Err(validateErr).Msg("error validating include/exclude lists")

		nagiosExitState.LastError = validateErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error validating include/exclude lists",
			nagios.StateCRITICALLabel,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	log.Debug().Msg("Retrieving eligible resource pools")
	resourcePools, getRPsErr := vsphere.GetEligibleRPs(
		ctx,
		c,
		cfg.IncludedResourcePools,
		cfg.ExcludedResourcePools,
		true,
	)
	if getRPsErr != nil {
		log.Error().Err(getRPsErr).Msg(
			"error retrieving list of resource pools",
		)

		nagiosExitState.LastError = getRPsErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error retrieving list of resource pools from %q",
			nagios.StateCRITICALLabel,
			cfg.Server,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	rpNames := make([]string, 0, len(resourcePools))
	for _, rp := range resourcePools {
		rpNames = append(rpNames, rp.Name)
	}

	log.Debug().
		Str("resource_pools", strings.Join(rpNames, ", ")).
		Msg("")

	log.Debug().Msg("Retrieving vms from eligible resource pools")
	vms, getVMsErr := vsphere.GetVMsFromRPs(ctx, c, resourcePools, true)
	if getVMsErr != nil {
		log.Error().Err(getVMsErr).Msg(
			"error retrieving list of VMs from resource pools list",
		)

		nagiosExitState.LastError = getVMsErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error retrieving list of VMs from resource pools list",
			nagios.StateCRITICALLabel,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	log.Debug().Msg("Drop any VMs we've been asked to exclude from checks")
	filteredVMs := vsphere.ExcludeVMsByName(vms, cfg.IgnoredVMs)

	vmNames := make([]string, 0, len(filteredVMs))
	for _, vm := range filteredVMs {
		vmNames = append(vmNames, vm.Name)
	}
	log.Debug().
		Str("virtual_machines", strings.Join(vmNames, ", ")).
		Msg("")

	log.Debug().Msg("Checking VMware Tools state")
	vmsWithIssues := vsphere.GetVMsWithToolsIssues(filteredVMs, cfg.PoweredOff)

	if len(vmsWithIssues) > 0 {

		log.Error().
			Int("vms_with_issues", len(vmsWithIssues)).
			Int("vms_total", len(vms)).
			Int("vms_filtered", len(filteredVMs)).
			Msg("issues with VMware Tools found")

		stateLabel, stateExitCode := vsphere.GetVMToolsStatusSummary(vmsWithIssues)

		nagiosExitState.LastError = fmt.Errorf(
			"%d of %d VMs with VMware Tools issues",
			len(vmsWithIssues),
			len(filteredVMs),
		)

		nagiosExitState.ServiceOutput = vsphere.VMToolsOneLineCheckSummary(
			stateLabel,
			vmsWithIssues,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.VMToolsReport(
			c,
			vms,
			filteredVMs,
			vmsWithIssues,
			cfg.IgnoredVMs,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = stateExitCode

		return

	}

	// success if we made it here

	log.Debug().
		Int("vms_total", len(vms)).
		Int("vms_filtered", len(filteredVMs)).
		Msg("No problems with VMware Tools found")

	nagiosExitState.LastError = nil

	nagiosExitState.ServiceOutput = vsphere.VMToolsOneLineCheckSummary(
		nagios.StateOKLabel,
		vmsWithIssues,
		filteredVMs,
		resourcePools,
	)

	nagiosExitState.LongServiceOutput = vsphere.VMToolsReport(
		c,
		vms,
		filteredVMs,
		vmsWithIssues,
		cfg.IgnoredVMs,
		cfg.IncludedResourcePools,
		cfg.ExcludedResourcePools,
		resourcePools,
	)

	nagiosExitState.ExitStatusCode = nagios.StateOKExitCode

}
