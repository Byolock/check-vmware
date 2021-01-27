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
	cfg, cfgErr := config.New(config.PluginType{SnapshotsAge: true})
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
		"%d day old snapshots present",
		cfg.SnapshotsAgeCritical,
	)

	nagiosExitState.WarningThreshold = fmt.Sprintf(
		"%d day old snapshots present",
		cfg.SnapshotsAgeWarning,
	)

	if cfg.EmitBranding {
		// If enabled, show application details at end of notification
		nagiosExitState.BrandingCallback = config.Branding("Notification generated by ")
	}

	log := cfg.Log.With().
		Str("included_resource_pools", cfg.IncludedResourcePools.String()).
		Str("excluded_resource_pools", cfg.ExcludedResourcePools.String()).
		Str("ignored_vms", cfg.IgnoredVMs.String()).
		Int("snapshots_age_critical", cfg.SnapshotsAgeCritical).
		Int("snapshots_age_warning", cfg.SnapshotsAgeWarning).
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

	defer func() {
		if err := c.Logout(ctx); err != nil {
			log.Error().
				Err(err).
				Msg("failed to logout")
		}
	}()

	// At this point we're logged in, ready to retrieve a list of VMs. If
	// specified, we should limit VMs based on include/exclude lists. First,
	// we'll make sure that all specified resource pools actually exist in the
	// vSphere environment.

	log.Debug().Msg("Validating resource pools")
	validateErr := vsphere.ValidateRPs(ctx, c.Client, cfg.IncludedResourcePools, cfg.ExcludedResourcePools)
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
		c.Client,
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
	vms, getVMsErr := vsphere.GetVMsFromRPs(ctx, c.Client, resourcePools, true)
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

	// NOTE: This plugin is hard-coded to evaluate powered off and powered
	// on VMs equally. I'm not sure whether ignoring powered off VMs by
	// default makes sense for this particular plugin.
	//
	// Please share your feedback on this GitHub issue if you feel differently:
	// https://github.com/atc0005/check-vmware/issues/79
	//
	// Please expand on some use cases for ignoring powered off VMs by default.
	//
	// log.Debug().Msg("Filter VMs to specified power state")
	// filteredVMs = vsphere.FilterVMsByPowerState(filteredVMs, cfg.PoweredOff)

	vmNames := make([]string, 0, len(filteredVMs))
	for _, vm := range filteredVMs {
		vmNames = append(vmNames, vm.Name)
	}
	log.Debug().Str("virtual_machines", strings.Join(vmNames, ", ")).Msg("")

	log.Debug().Msg("Filter VMs to those with snapshots")
	vmsWithSnapshots := vsphere.FilterVMsWithSnapshots(filteredVMs)

	log.Debug().Msg("Build snapshot sets for bulk processing")
	snapshotSets := make(vsphere.SnapshotSummarySets, 0, len(vmsWithSnapshots))

	for _, vm := range vmsWithSnapshots {

		log.Debug().Str("vm", vm.Name).Msg("Evaluating snapshots for VM")

		snapshotSets = append(
			snapshotSets,
			vsphere.NewSnapshotSummarySet(
				vm,
				cfg.SnapshotsAgeCritical,
				cfg.SnapshotsAgeWarning,
				cfg.SnapshotsSizeCritical,
				cfg.SnapshotsSizeWarning,

				// revisit with GH-76
				false,
			),
		)
	}

	switch {

	case snapshotSets.IsAgeCriticalState():

		log.Error().
			Int("num_snapshots_age_critical", snapshotSets.ExceedsAge(cfg.SnapshotsAgeCritical)).
			Msg("Snapshot sets contain a snapshot which exceeds specified age in days")

		nagiosExitState.LastError = vsphere.ErrSnapshotAgeThresholdCrossed

		nagiosExitState.ServiceOutput = vsphere.SnapshotsAgeOneLineCheckSummary(
			nagios.StateCRITICALLabel,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.SnapshotsAgeReport(
			c.Client,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			vms,
			filteredVMs,
			vmsWithSnapshots,
			cfg.IgnoredVMs,
			cfg.PoweredOff,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return

	case snapshotSets.IsAgeWarningState():

		log.Error().
			Int("num_snapshots_age_warning", snapshotSets.ExceedsAge(cfg.SnapshotsAgeWarning)).
			Msg("Snapshot sets contain one or more snapshots which exceed specified age in days")

		nagiosExitState.LastError = vsphere.ErrSnapshotAgeThresholdCrossed

		nagiosExitState.ServiceOutput = vsphere.SnapshotsAgeOneLineCheckSummary(
			nagios.StateWARNINGLabel,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.SnapshotsAgeReport(
			c.Client,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			vms,
			filteredVMs,
			vmsWithSnapshots,
			cfg.IgnoredVMs,
			cfg.PoweredOff,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateWARNINGExitCode

		return

	default:

		nagiosExitState.LastError = nil

		nagiosExitState.ServiceOutput = vsphere.SnapshotsAgeOneLineCheckSummary(
			nagios.StateOKLabel,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.SnapshotsAgeReport(
			c.Client,
			snapshotSets,
			cfg.SnapshotsAgeCritical,
			cfg.SnapshotsAgeWarning,
			vms,
			filteredVMs,
			vmsWithSnapshots,
			cfg.IgnoredVMs,
			cfg.PoweredOff,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
		)

		nagiosExitState.ExitStatusCode = nagios.StateOKExitCode

		return

	}

}
