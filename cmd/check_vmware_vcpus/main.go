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
	cfg, cfgErr := config.New(config.PluginType{VirtualCPUsAllocation: true})
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

	// Record thresholds for use as Nagios "Long Service Output" content. This
	// content is shown in the detailed web UI and in notifications generated
	// by Nagios.
	nagiosExitState.CriticalThreshold = fmt.Sprintf(
		"%d%% of %d vCPUs allocated",
		cfg.VCPUsAllocatedCritical,
		cfg.VCPUsMaxAllowed,
	)

	nagiosExitState.WarningThreshold = fmt.Sprintf(
		"%d%% of %d vCPUs allocated",
		cfg.VCPUsAllocatedWarning,
		cfg.VCPUsMaxAllowed,
	)

	if cfg.EmitBranding {
		// If enabled, show application details at end of notification
		nagiosExitState.BrandingCallback = config.Branding("Notification generated by ")
	}

	log := cfg.Log.With().
		Str("included_resource_pools", cfg.IncludedResourcePools.String()).
		Str("excluded_resource_pools", cfg.ExcludedResourcePools.String()).
		Str("ignored_vms", cfg.IgnoredVMs.String()).
		Bool("eval_powered_off", cfg.PoweredOff).
		Int("max_vcpus_allowed", cfg.VCPUsMaxAllowed).
		Int("vcpus_critical_allocation", cfg.VCPUsAllocatedCritical).
		Int("vcpus_warning_allocation", cfg.VCPUsAllocatedWarning).
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

	log.Debug().Msg("Filter VMs to specified power state")
	filteredVMs = vsphere.FilterVMsByPowerState(filteredVMs, cfg.PoweredOff)

	vmNames := make([]string, 0, len(filteredVMs))
	for _, vm := range filteredVMs {
		vmNames = append(vmNames, vm.Name)
	}
	log.Debug().
		Str("virtual_machines", strings.Join(vmNames, ", ")).
		Msg("")

	// here we diverge from VMware Tools plugin

	var vCPUsAllocated int32
	for _, vm := range filteredVMs {
		vCPUsAllocated += vm.Summary.Config.NumCpu
		log.Debug().
			Str("vm_name", vm.Name).
			Int32("num_vcpu", vm.Summary.Config.NumCpu).
			Msg("")
	}

	log.Debug().
		Int32("vcpus_allocated", vCPUsAllocated).
		Msg("Finished counting vCPUs")

	vCPUsPercentageUsedOfAllowed := float32(vCPUsAllocated) / float32(cfg.VCPUsMaxAllowed) * 100
	var vCPUsRemaining int32

	switch {
	case vCPUsAllocated > int32(cfg.VCPUsMaxAllowed):
		vCPUsRemaining = 0
	default:
		vCPUsRemaining = int32(cfg.VCPUsMaxAllowed) - vCPUsAllocated
	}

	log.Debug().
		Float32("vcpus_percent_used", vCPUsPercentageUsedOfAllowed).
		Int32("vcpus_remaining", vCPUsRemaining).
		Msg("")

	switch {
	case vCPUsPercentageUsedOfAllowed > float32(cfg.VCPUsAllocatedCritical):

		log.Error().
			Float32("vcpus_percent_used", vCPUsPercentageUsedOfAllowed).
			Int32("vcpus_remaining", vCPUsRemaining).
			Int("vms_filtered", len(filteredVMs)).
			Msg("vCPUs allocation")

		nagiosExitState.LastError = fmt.Errorf(
			"%d of %d vCPUs allocated (%0.1f%% more than allowed)",
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			vCPUsPercentageUsedOfAllowed,
		)

		nagiosExitState.ServiceOutput = vsphere.VirtualCPUsOneLineCheckSummary(
			nagios.StateCRITICALLabel,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			filteredVMs, resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.VirtualCPUsReport(
			c,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			vms,
			filteredVMs,
			cfg.IgnoredVMs,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return

	case vCPUsPercentageUsedOfAllowed > float32(cfg.VCPUsAllocatedWarning):

		log.Error().
			Float32("vcpus_percent_used", vCPUsPercentageUsedOfAllowed).
			Int32("vcpus_remaining", vCPUsRemaining).
			Int("vms_filtered", len(filteredVMs)).
			Msg("vCPUs allocation warning")

		nagiosExitState.LastError = fmt.Errorf(
			"%d of %d vCPUs allocated (%0.1f%% more than allowed)",
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			vCPUsPercentageUsedOfAllowed,
		)

		nagiosExitState.ServiceOutput = vsphere.VirtualCPUsOneLineCheckSummary(
			nagios.StateWARNINGLabel,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			filteredVMs, resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.VirtualCPUsReport(
			c,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			vms,
			filteredVMs,
			cfg.IgnoredVMs,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateWARNINGExitCode

		return

	default:

		nagiosExitState.LastError = nil

		nagiosExitState.ServiceOutput = vsphere.VirtualCPUsOneLineCheckSummary(
			nagios.StateOKLabel,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			filteredVMs, resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.VirtualCPUsReport(
			c,
			vCPUsAllocated,
			cfg.VCPUsMaxAllowed,
			vms,
			filteredVMs,
			cfg.IgnoredVMs,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateOKExitCode

		return

	}

}
