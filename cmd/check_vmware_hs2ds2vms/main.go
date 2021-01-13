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
	"sort"
	"strings"

	"github.com/atc0005/go-nagios"

	"github.com/atc0005/check-vmware/internal/config"
	"github.com/atc0005/check-vmware/internal/textutils"
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
	cfg, cfgErr := config.New(config.PluginType{Host2Datastores2VMs: true})
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
	nagiosExitState.CriticalThreshold = "Any errors encountered or Hosts/Datastores/VMs mismatches."
	nagiosExitState.WarningThreshold = "Not used by this plugin."

	if cfg.EmitBranding {
		// If enabled, show application details at end of notification
		nagiosExitState.BrandingCallback = config.Branding("Notification generated by ")
	}

	log := cfg.Log.With().
		Str("included_resource_pools", cfg.IncludedResourcePools.String()).
		Str("excluded_resource_pools", cfg.ExcludedResourcePools.String()).
		Str("ignored_vms", cfg.IgnoredVMs.String()).
		Bool("eval_powered_off", cfg.PoweredOff).
		Bool("ignore_missing_ca_on_objects", cfg.IgnoreMissingCustomAttribute).
		Str("datastore_ca_name", cfg.DatastoreCAName()).
		Str("datastore_ca_prefix_separator", cfg.DatastoreCASep()).
		Str("host_ca_name", cfg.HostCAName()).
		Str("host_ca_prefix_separator", cfg.HostCASep()).
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

	log.Debug().Msg("Filter VMs to specified power state")
	filteredVMs = vsphere.FilterVMsByPowerState(filteredVMs, cfg.PoweredOff)

	vmNames := make([]string, 0, len(filteredVMs))
	for _, vm := range filteredVMs {
		vmNames = append(vmNames, vm.Name)
	}
	log.Debug().
		Str("virtual_machines", strings.Join(vmNames, ", ")).
		Msg("")

	// here we diverge from other plugins

	dss, dssErr := vsphere.GetDatastores(ctx, c.Client, true)
	if dssErr != nil {
		log.Error().Err(dssErr).Msg(
			"error retrieving list of datastores",
		)

		nagiosExitState.LastError = dssErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error retrieving list of datastores",
			nagios.StateCRITICALLabel,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	dsNames := make([]string, 0, len(dss))
	for _, ds := range dss {
		dsNames = append(dsNames, ds.Name)
	}

	// validate the list of ignored datastores
	if len(cfg.IgnoredDatastores) > 0 {
		for _, ignDSName := range cfg.IgnoredDatastores {
			if !textutils.InList(ignDSName, dsNames, true) {

				validateIgnoredDSErr := fmt.Errorf(
					"error validating list of ignored datastores",
				)
				validateIgnoredDSErrMsg := fmt.Sprintf(
					"datastore %s could not be ignored as requested; "+
						"could not locate datastore within vSphere inventory",
					ignDSName,
				)
				log.Error().Err(validateIgnoredDSErr).
					Msg(validateIgnoredDSErrMsg)

				nagiosExitState.LastError = validateIgnoredDSErr
				nagiosExitState.ServiceOutput = fmt.Sprintf(
					"%s: %s",
					nagios.StateCRITICALLabel,
					validateIgnoredDSErrMsg,
				)
				nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

				return

			}
		}
	}

	hss, hsErr := vsphere.GetHostSystems(ctx, c.Client, true)
	if hsErr != nil {
		log.Error().Err(hsErr).Msg(
			"error retrieving list of hosts",
		)

		nagiosExitState.LastError = hsErr
		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: Error retrieving list of hosts",
			nagios.StateCRITICALLabel,
		)
		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return
	}

	dsCustomAttributeName := cfg.DatastoreCAName()
	datastores := make([]vsphere.DatastoreWithCA, 0, len(dss))

	for _, ds := range dss {

		// if user opted to ignore the Datastore, skip attempts to retrieve
		// Custom Attribute for it.
		if textutils.InList(ds.Name, cfg.IgnoredDatastores, true) {
			continue
		}

		caVal, caValErr := vsphere.GetObjectCAVal(dsCustomAttributeName, ds.ManagedEntity)
		if caValErr != nil {
			switch {

			case errors.Is(caValErr, vsphere.ErrCustomAttributeNotSet):

				// emit message even if the plugin is allowed to ignore the
				// problem; this is not shown in the UI or notifications, only
				// if running the plugin manually.
				log.Error().Err(caValErr).
					Str("custom_attribute_name", dsCustomAttributeName).
					Str("datastore", ds.Name).
					Msg("specified Custom Attribute not set on datastore")

				if !cfg.IgnoreMissingCustomAttribute {
					nagiosExitState.LastError = caValErr
					nagiosExitState.ServiceOutput = fmt.Sprintf(
						"%s: Custom Attribute %q not set on datastore %q",
						nagios.StateCRITICALLabel,
						dsCustomAttributeName,
						ds.Name,
					)
					nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

					return
				}

				caVal = vsphere.CustomAttributeValNotSet

			// custom attributes are set, but some other error occurred
			case caValErr != nil:

				log.Error().Err(caValErr).
					Str("custom_attribute_name", dsCustomAttributeName).
					Msg("error retrieving value for provided Custom Attribute")

				nagiosExitState.LastError = caValErr
				nagiosExitState.ServiceOutput = fmt.Sprintf(
					"%s: Error retrieving value for provided Custom Attribute %q",
					nagios.StateCRITICALLabel,
					dsCustomAttributeName,
				)
				nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

				return

			}
		}

		datastores = append(datastores, vsphere.DatastoreWithCA{
			Datastore: ds,
			CustomAttribute: vsphere.PairingCustomAttribute{
				Name:  dsCustomAttributeName,
				Value: caVal,
			},
		})
	}

	for _, ds := range datastores {
		log.Debug().
			Str("datastore", ds.Name).
			Str("custom_attribute_name", ds.CustomAttribute.Name).
			Str("custom_attribute_value", ds.CustomAttribute.Value).
			Msg("")
	}

	hostCustomAttributeName := cfg.HostCAName()
	hosts := make([]vsphere.HostWithCA, 0, len(hss))
	for _, host := range hss {
		caVal, caValErr := vsphere.GetObjectCAVal(hostCustomAttributeName, host.ManagedEntity)
		if caValErr != nil {
			switch {

			case errors.Is(caValErr, vsphere.ErrCustomAttributeNotSet):

				// emit message even if the plugin is allowed to ignore the
				// problem (reminder: this is not shown in the UI or
				// notifications, only if running the plugin manually)
				log.Error().Err(caValErr).
					Str("custom_attribute_name", hostCustomAttributeName).
					Str("host", host.Name).
					Msg("specified Custom Attribute not set on host")

				if !cfg.IgnoreMissingCustomAttribute {
					nagiosExitState.LastError = caValErr
					nagiosExitState.ServiceOutput = fmt.Sprintf(
						"%s: Custom Attribute %q not set on host %q",
						nagios.StateCRITICALLabel,
						hostCustomAttributeName,
						host.Name,
					)
					nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

					return
				}

				caVal = vsphere.CustomAttributeValNotSet

			case caValErr != nil:
				log.Error().Err(caValErr).
					Str("custom_attribute_name", hostCustomAttributeName).
					Str("host", host.Name).
					Msg("error retrieving value for provided Custom Attribute")

				nagiosExitState.LastError = caValErr
				nagiosExitState.ServiceOutput = fmt.Sprintf(
					"%s: Error retrieving value for provided Custom Attribute %q from host %q",
					nagios.StateCRITICALLabel,
					hostCustomAttributeName,
					host.Name,
				)
				nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

				return
			}
		}

		// here is where you decide on whether we're processing prefixes

		hosts = append(hosts, vsphere.HostWithCA{
			HostSystem: host,
			CustomAttribute: vsphere.PairingCustomAttribute{
				Name:  hostCustomAttributeName,
				Value: caVal,
			},
		})

	}

	for _, host := range hosts {
		log.Debug().
			Str("host", host.Name).
			Str("custom_attribute_name", host.CustomAttribute.Name).
			Str("custom_attribute_value", host.CustomAttribute.Value).
			Msg("")
	}

	h2dIdx, h2dIdxErr := vsphere.NewHostToDatastoreIndex(
		hosts,
		datastores,
		cfg.UsingCAPrefixes(),
		cfg.HostCASep(),
		cfg.DatastoreCASep(),
	)

	// make sure we have at least one pairing, otherwise bail
	//
	// TODO: Should this instead be a length check and we explicitly return
	// the vsphere.ErrHostDatastorePairingFailed error, or keep this like it
	// is?
	if h2dIdxErr != nil {

		var errMsg string
		switch {
		case errors.Is(h2dIdxErr, vsphere.ErrHostDatastorePairingFailed):
			errMsg = "no matching datastores and hosts found using provided Custom Attribute"

		default:
			errMsg = "unknown error encountered while evaluating datastores and hosts for Custom Attribute"
		}

		log.Error().Err(h2dIdxErr).Msg(errMsg)

		nagiosExitState.ServiceOutput = fmt.Sprintf(
			"%s: %s [host: %q, datastore: %q]",
			nagios.StateCRITICALLabel,
			errMsg,
			hostCustomAttributeName,
			dsCustomAttributeName,
		)

		nagiosExitState.LastError = h2dIdxErr

		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return

	}

	for hostID, pairing := range h2dIdx {

		dsNamesForHost := func(pairings vsphere.HostDatastoresPairing) string {
			names := make([]string, len(pairing.Datastores))
			for i := range pairing.Datastores {
				names[i] = pairing.Datastores[i].Name
			}
			return strings.Join(names, ", ")
		}(pairing)

		log.Debug().
			Str("host", h2dIdx[hostID].Host.Name).
			Str("datastores", dsNamesForHost).
			Msg("host/datastores pairing")
	}

	// now process VMs
	vmDatastoresPairingIssues := make(vsphere.VMToMismatchedDatastoreNames)
	// var vmsWithPairingIssues []mo.VirtualMachine
	for _, vm := range filteredVMs {

		hostName := h2dIdx[vm.Runtime.Host.Value].Host.Name

		mismatchedDatastores, lookupErr := h2dIdx.ValidateVirtualMachinePairings(
			vm.Summary.Runtime.Host.Value,
			dss,
			vm.Datastore,
			cfg.IgnoredDatastores,
		)

		if lookupErr != nil {

			log.Error().Err(lookupErr).
				// Str("extended_error_msg", extendedErrMsg).
				Str("vm", vm.Name).
				// Str("datastore", datastore.Name).
				Msg("Error occurred while validating VM/Host/Datastore match")

			nagiosExitState.LastError = lookupErr
			nagiosExitState.ServiceOutput = fmt.Sprintf(
				"%s: Error occurred while validating VM/Host/Datastore match for VM %s on host %s",
				nagios.StateCRITICALLabel,
				vm.Name,
				hostName,
			)
			nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

			return
		}

		vmDatastoreNames := vsphere.DatastoreIDsToNames(vm.Datastore, dss)

		switch {
		// expected failure scenario; set LongServiceOutput using report func
		case len(mismatchedDatastores) > 0:

			// TODO: Should this be error level and shown by default, or debug
			// level and muted?
			log.Debug().
				Str("host", hostName).
				Str("vm", vm.Name).
				Bool("validation_failed", true).
				Str("vm_datastores_all", strings.Join(vmDatastoreNames, ", ")).
				Str("vm_datastores_mismatched", strings.Join(mismatchedDatastores, ", ")).
				Msg("VM/Host/Datastore validation failed")

			// collect problem VirtualMachine objects for later review
			// vmsWithPairingIssues = append(vmsWithPairingIssues, vm)

			// index mismatched datastore names by VirtualMachine name, also for
			// later review
			vmDatastoresPairingIssues[vm.Name] = vsphere.VMHostDatastoresPairing{
				HostName:       hostName,
				DatastoreNames: mismatchedDatastores,
			}

		default:

			log.Debug().
				Str("host", hostName).
				Str("vm", vm.Name).
				Bool("validation_failed", false).
				Str("vm_datastores_all", strings.Join(vmDatastoreNames, ", ")).
				Msg("all datastores for vm matched to current host")

		}
	}

	numMismatches := len(vmDatastoresPairingIssues)

	switch {
	// expected failure scenario; set LongServiceOutput using report func
	case numMismatches > 0:

		var vmNames []string
		for vmName := range vmDatastoresPairingIssues {
			vmNames = append(vmNames, vmName)
		}
		sort.Strings(vmNames)

		log.Error().
			Int("mismatched_vms_count", numMismatches).
			Str("mismatched_vms_list", strings.Join(vmNames, ", ")).
			Msg("VM/Host/Datastore validation failed")

		nagiosExitState.LastError = vsphere.ErrVMDatastoreNotInVMHostPairedList

		nagiosExitState.ServiceOutput = vsphere.H2D2VMsOneLineCheckSummary(
			nagios.StateCRITICALLabel,
			vmDatastoresPairingIssues,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.H2D2VMsReport(
			c.Client,
			h2dIdx,
			vms,
			filteredVMs,
			vmDatastoresPairingIssues,
			cfg.IgnoredVMs,
			cfg.PoweredOff,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
			cfg.IgnoreMissingCustomAttribute,
			cfg.IgnoredDatastores,
			cfg.DatastoreCASep(),
			cfg.HostCASep(),
			cfg.DatastoreCAName(),
			cfg.HostCAName(),
		)

		nagiosExitState.ExitStatusCode = nagios.StateCRITICALExitCode

		return

	default:
		// success if we made it here

		log.Debug().
			Int("vms_total", len(vms)).
			Int("vms_filtered", len(filteredVMs)).
			Msg("No problems with VMware Tools found")

		nagiosExitState.LastError = nil

		nagiosExitState.ServiceOutput = vsphere.H2D2VMsOneLineCheckSummary(
			nagios.StateOKLabel,
			vmDatastoresPairingIssues,
			filteredVMs,
			resourcePools,
		)

		nagiosExitState.LongServiceOutput = vsphere.H2D2VMsReport(
			c.Client,
			h2dIdx,
			vms,
			filteredVMs,
			vmDatastoresPairingIssues,
			cfg.IgnoredVMs,
			cfg.PoweredOff,
			cfg.IncludedResourcePools,
			cfg.ExcludedResourcePools,
			resourcePools,
			cfg.IgnoreMissingCustomAttribute,
			cfg.IgnoredDatastores,
			cfg.DatastoreCASep(),
			cfg.HostCASep(),
			cfg.DatastoreCAName(),
			cfg.HostCAName(),
		)

		nagiosExitState.ExitStatusCode = nagios.StateOKExitCode

	}

}