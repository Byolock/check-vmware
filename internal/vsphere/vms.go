// Copyright 2021 Adam Chalkley
//
// https://github.com/atc0005/check-vmware
//
// Licensed under the MIT License. See LICENSE file in the project root for
// full license information.

package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atc0005/check-vmware/internal/textutils"
	"github.com/atc0005/go-nagios"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// ErrVirtualMachinePowerCycleUptimeThresholdCrossed indicates that specified
// Virtual Machine power cycle thresholds have been exceeeded.
var ErrVirtualMachinePowerCycleUptimeThresholdCrossed = errors.New("power cycle uptime exceeds specified threshold")

// VirtualMachinePowerCycleUptimeStatus tracks VirtualMachines with power
// cycle uptimes that exceed specified thresholds.
type VirtualMachinePowerCycleUptimeStatus struct {
	VMsCritical       []mo.VirtualMachine
	VMsWarning        []mo.VirtualMachine
	WarningThreshold  int
	CriticalThreshold int
}

// VMNames returns a list of sorted VirtualMachine names which have exceeded
// specified power cycle uptime thresholds.
func (vpcs VirtualMachinePowerCycleUptimeStatus) VMNames() string {
	vmNames := make([]string, 0, len(vpcs.VMsCritical)+len(vpcs.VMsWarning))

	for _, vm := range vpcs.VMsWarning {
		vmNames = append(vmNames, vm.Name)
	}
	for _, vm := range vpcs.VMsCritical {
		vmNames = append(vmNames, vm.Name)
	}

	sort.Slice(vmNames, func(i, j int) bool {
		return strings.ToLower(vmNames[i]) < strings.ToLower(vmNames[j])
	})

	return strings.Join(vmNames, ", ")
}

// GetVMs accepts a context, a connected client and a boolean value indicating
// whether a subset of properties per VirtualMachine are retrieved. If
// requested, a subset of all available properties will be retrieved (faster)
// instead of recursively fetching all properties (about 2x as slow) A
// collection of VirtualMachines with requested properties is returned or nil
// and an error, if one occurs.
func GetVMs(ctx context.Context, c *vim25.Client, propsSubset bool) ([]mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	// declare this early so that we can grab a pointer to it in order to
	// access the entries later
	// vms := make([]mo.VirtualMachine, 0, 100)
	var vms []mo.VirtualMachine

	defer func(vms *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute GetVMs func (and retrieve %d VirtualMachines).\n",
			time.Since(funcTimeStart),
			len(*vms),
		)
	}(&vms)

	err := getObjects(ctx, c, &vms, c.ServiceContent.RootFolder, propsSubset)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve VirtualMachines: %w", err)
	}

	sort.Slice(vms, func(i, j int) bool {
		return strings.ToLower(vms[i].Name) < strings.ToLower(vms[j].Name)
	})

	return vms, nil
}

// GetVMsFromContainer receives one or many ManagedEntity values for Folder,
// Datacenter, ComputeResource, ResourcePool, or HostSystem types and returns
// a list of VirtualMachine object references.
//
// The propsSubset boolean value indicates whether a subset of properties per
// VirtualMachine are retrieved. If requested, a subset of all available
// properties will be retrieved (faster) instead of recursively fetching all
// properties (about 2x as slow). A collection of VirtualMachines with
// requested properties is returned or nil and an error, if one occurs.
func GetVMsFromContainer(ctx context.Context, c *vim25.Client, propsSubset bool, objs ...mo.ManagedEntity) ([]mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	// declare this early so that we can grab a pointer to it in order to
	// access the entries later
	var vms []mo.VirtualMachine

	defer func(vms *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute GetVMsFromContainers func (and retrieve %d VMs).\n",
			time.Since(funcTimeStart),
			len(*vms),
		)
	}(&vms)

	for _, obj := range objs {

		err := getObjects(ctx, c, &vms, obj.Reference(), propsSubset)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to retrieve VirtualMachines from object: %s: %w",
				obj.Name,
				err,
			)
		}
	}

	// remove any potential duplicate entries which could occur if we are
	// evaluating the (default, hidden) 'Resources' Resource Pool
	vms = dedupeVMs(vms)

	sort.Slice(vms, func(i, j int) bool {
		return strings.ToLower(vms[i].Name) < strings.ToLower(vms[j].Name)
	})

	return vms, nil

}

// GetVMsFromDatastore receives a Datastore object reference and returns a
// list of VirtualMachine object references. The propsSubset boolean value
// indicates whether a subset of properties per VirtualMachine are retrieved.
// If requested, a subset of all available properties will be retrieved
// (faster) instead of recursively fetching all properties (about 2x as slow)
// A collection of VirtualMachines with requested properties is returned or
// nil and an error, if one occurs.
func GetVMsFromDatastore(ctx context.Context, c *vim25.Client, ds mo.Datastore, propsSubset bool) ([]mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	// declare this early so that we can grab a pointer to it in order to
	// access the entries later
	dsVMs := make([]mo.VirtualMachine, len(ds.Vm))

	defer func(vms *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute GetVMsFromDatastore func (and retrieve %d VMs).\n",
			time.Since(funcTimeStart),
			len(*vms),
		)
	}(&dsVMs)

	var allVMs []mo.VirtualMachine
	err := getObjects(ctx, c, &allVMs, c.ServiceContent.RootFolder, propsSubset)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to retrieve VirtualMachines from Datastore %s: %w",
			ds.Name,
			err,
		)
	}

	for i := range ds.Vm {
		vm, err := FilterVMByID(allVMs, ds.Vm[i].Value)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to retrieve VM for VM ID %s: %w",
				ds.Vm[i].Value,
				err,
			)
		}

		dsVMs[i] = vm
	}

	sort.Slice(dsVMs, func(i, j int) bool {
		return strings.ToLower(dsVMs[i].Name) < strings.ToLower(dsVMs[j].Name)
	})

	return dsVMs, nil

}

// GetVMByName accepts the name of a VirtualMachine, the name of a datacenter
// and a boolean value indicating whether only a subset of properties for the
// VirtualMachine should be returned. If requested, a subset of all available
// properties will be retrieved (faster) instead of recursively fetching all
// properties (about 2x as slow). If the datacenter name is an empty string
// then the default datacenter will be used.
func GetVMByName(ctx context.Context, c *vim25.Client, vmName string, datacenter string, propsSubset bool) (mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute GetVMByName func.\n",
			time.Since(funcTimeStart),
		)
	}()

	var vm mo.VirtualMachine
	err := getObjectByName(ctx, c, &vm, vmName, datacenter, propsSubset)

	if err != nil {
		return mo.VirtualMachine{}, err
	}

	return vm, nil

}

// FilterVMByName accepts a collection of VirtualMachines and a VirtualMachine
// name to filter against. An error is returned if the list of VirtualMachines
// is empty or if a match was not found.
func FilterVMByName(vms []mo.VirtualMachine, vmName string) (mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute FilterVMByName func.\n",
			time.Since(funcTimeStart),
		)
	}()

	if len(vms) == 0 {
		return mo.VirtualMachine{}, fmt.Errorf("received empty list of virtual machines to filter by name")
	}

	for _, vm := range vms {
		if vm.Name == vmName {
			return vm, nil
		}
	}

	return mo.VirtualMachine{}, fmt.Errorf(
		"error: failed to retrieve VirtualMachine using provided name %q",
		vmName,
	)

}

// FilterVMByID receives a collection of VirtualMachines and a VirtualMachine
// ID to filter against. An error is returned if the list of VirtualMachines
// is empty or if a match was not found.
func FilterVMByID(vms []mo.VirtualMachine, vmID string) (mo.VirtualMachine, error) {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute FilterVMByID func.\n",
			time.Since(funcTimeStart),
		)
	}()

	if len(vms) == 0 {
		return mo.VirtualMachine{}, fmt.Errorf("received empty list of virtual machines to filter by ID")
	}

	for _, vm := range vms {
		// return match, if available
		if vm.Summary.Vm.Value == vmID {
			return vm, nil
		}
	}

	return mo.VirtualMachine{}, fmt.Errorf(
		"error: failed to retrieve VirtualMachine using provided ID %q",
		vmID,
	)

}

// ExcludeVMsByName receives a collection of VirtualMachines and a list of VMs
// that should be ignored. A new collection minus ignored VirtualMachines is
// returned. If the collection of VirtualMachine is empty, an empty collection
// is returned. If the list of ignored VirtualMachines is empty, the same
// items from the received collection of VirtualMachines is returned. If the
// list of ignored VirtualMachines is greater than the list of received
// VirtualMachines, then only matching VirtualMachines will be excluded and
// any others silently skipped.
func ExcludeVMsByName(allVMs []mo.VirtualMachine, ignoreList []string) []mo.VirtualMachine {

	if len(allVMs) == 0 || len(ignoreList) == 0 {
		return allVMs
	}

	vmsToKeep := make([]mo.VirtualMachine, 0, len(allVMs))

	for _, vm := range allVMs {
		if textutils.InList(vm.Name, ignoreList, true) {
			continue
		}
		vmsToKeep = append(vmsToKeep, vm)
	}

	sort.Slice(vmsToKeep, func(i, j int) bool {
		return strings.ToLower(vmsToKeep[i].Name) < strings.ToLower(vmsToKeep[j].Name)
	})

	return vmsToKeep

}

// FilterVMsByPowerState accepts a collection of VirtualMachines and a boolean
// value to indicate whether powered off VMs should be included in the
// returned collection. If the collection of provided VirtualMachines is
// empty, an empty collection is returned.
func FilterVMsByPowerState(vms []mo.VirtualMachine, includePoweredOff bool) []mo.VirtualMachine {

	// setup early so we can reference it from deferred stats output
	filteredVMs := make([]mo.VirtualMachine, 0, len(vms))

	funcTimeStart := time.Now()

	defer func(vms []mo.VirtualMachine, filteredVMs *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute FilterVMsByPowerState func (for %d VMs, yielding %d VMs)\n",
			time.Since(funcTimeStart),
			len(vms),
			len(*filteredVMs),
		)
	}(vms, &filteredVMs)

	if len(vms) == 0 {
		return vms
	}

	for _, vm := range vms {
		switch {
		// case includePoweredOff && vm.Guest.ToolsStatus != types.VirtualMachineToolsStatusToolsOk:
		// 	vmsWithIssues = append(vmsWithIssues, vm)

		case vm.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn:
			filteredVMs = append(filteredVMs, vm)

		case includePoweredOff &&
			vm.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOff:
			filteredVMs = append(filteredVMs, vm)

		}
	}

	return filteredVMs

}

// FilterVMsByPowerCycleUptime filters the provided collection of
// VirtualMachines to just those with WARNING or CRITICAL values based on
// provided thresholds.
func FilterVMsByPowerCycleUptime(vms []mo.VirtualMachine, warningThreshold int, criticalThreshold int) []mo.VirtualMachine {

	// setup early so we can reference it from deferred stats output
	var vmsWithIssues []mo.VirtualMachine

	funcTimeStart := time.Now()

	defer func(vms []mo.VirtualMachine, filteredVMs *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute FilterVMsByPowerCycleUptime func (for %d VMs, yielding %d VMs).\n",
			time.Since(funcTimeStart),
			len(vms),
			len(*filteredVMs),
		)
	}(vms, &vmsWithIssues)

	for _, vm := range vms {
		uptime := time.Duration(vm.Summary.QuickStats.UptimeSeconds) * time.Second
		uptimeDays := uptime.Hours() / 24

		// compare against the WARNING threshold as that will net VMs with
		// CRITICAL state as well.
		if uptimeDays > float64(warningThreshold) {
			vmsWithIssues = append(vmsWithIssues, vm)
		}
	}

	return vmsWithIssues

}

// dedupeVMs receives a list of VirtualMachine values potentially containing
// one or more duplicate values and returns a new list of unique
// VirtualMachine values.
//
// Credit:
// https://www.reddit.com/r/golang/comments/5ia523/idiomatic_way_to_remove_duplicates_in_a_slice/db6qa2e
func dedupeVMs(vmsList []mo.VirtualMachine) []mo.VirtualMachine {

	funcTimeStart := time.Now()

	defer func(vms *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute dedupeVMs func (evaluated %d VMs).\n",
			time.Since(funcTimeStart),
			len(*vms),
		)
	}(&vmsList)

	seen := make(map[string]struct{}, len(vmsList))
	j := 0
	for _, vm := range vmsList {
		if _, ok := seen[vm.Summary.Vm.Value]; ok {
			continue
		}
		seen[vm.Summary.Vm.Value] = struct{}{}
		vmsList[j] = vm
		j++
	}

	return vmsList[:j]
}

// GetVMPowerCycleUptimeStatusSummary accepts a list of VirtualMachines and
// threshold values and generates a collection of VirtualMachines that exceeds
// given thresholds along with those given thresholds.
func GetVMPowerCycleUptimeStatusSummary(
	vms []mo.VirtualMachine,
	warningThreshold int,
	criticalThreshold int,
) VirtualMachinePowerCycleUptimeStatus {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute GetVMPowerCycleUptimeStatusSummary func.\n",
			time.Since(funcTimeStart),
		)
	}()

	var vmsCritical []mo.VirtualMachine
	var vmsWarning []mo.VirtualMachine

	for _, vm := range vms {

		uptime := time.Duration(vm.Summary.QuickStats.UptimeSeconds) * time.Second
		uptimeDays := uptime.Hours() / 24

		switch {
		case uptimeDays > float64(criticalThreshold):
			vmsCritical = append(vmsCritical, vm)

		case uptimeDays > float64(warningThreshold):
			vmsWarning = append(vmsWarning, vm)

		}

	}

	return VirtualMachinePowerCycleUptimeStatus{
		VMsCritical:       vmsCritical,
		VMsWarning:        vmsWarning,
		WarningThreshold:  warningThreshold,
		CriticalThreshold: criticalThreshold,
	}

}

// VMPowerCycleUptimeOneLineCheckSummary is used to generate a one-line Nagios
// service check results summary. This is the line most prominent in
// notifications.
func VMPowerCycleUptimeOneLineCheckSummary(
	stateLabel string,
	evaluatedVMs []mo.VirtualMachine,
	uptimeSummary VirtualMachinePowerCycleUptimeStatus,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute VMPowerCycleUptimeOneLineCheckSummary func.\n",
			time.Since(funcTimeStart),
		)
	}()

	switch {
	case len(uptimeSummary.VMsCritical) > 0:
		return fmt.Sprintf(
			"%s: %d VMs with power cycle uptime exceeding %d days detected (evaluated %d VMs, %d Resource Pools)",
			stateLabel,
			len(uptimeSummary.VMsCritical),
			uptimeSummary.CriticalThreshold,
			len(evaluatedVMs),
			len(rps),
		)

	case len(uptimeSummary.VMsWarning) > 0:
		return fmt.Sprintf(
			"%s: %d VMs with power cycle uptime exceeding %d days detected (evaluated %d VMs, %d Resource Pools)",
			stateLabel,
			len(uptimeSummary.VMsWarning),
			uptimeSummary.WarningThreshold,
			len(evaluatedVMs),
			len(rps),
		)

	default:

		return fmt.Sprintf(
			"%s: No VMs with power cycle uptime exceeding %d days detected (evaluated %d VMs, %d Resource Pools)",
			stateLabel,
			uptimeSummary.WarningThreshold,
			len(evaluatedVMs),
			len(rps),
		)
	}
}

// VMPowerCycleUptimeReport generates a summary of VMs which exceed power
// cycle uptime thresholds along with various verbose details intended to aid
// in troubleshooting check results at a glance. This information is provided
// for use with the Long Service Output field commonly displayed on the
// detailed service check results display in the web UI or in the body of many
// notifications.
func VMPowerCycleUptimeReport(
	c *vim25.Client,
	allVMs []mo.VirtualMachine,
	evaluatedVMs []mo.VirtualMachine,
	uptimeSummary VirtualMachinePowerCycleUptimeStatus,
	vmsToExclude []string,
	evalPoweredOffVMs bool,
	includeRPs []string,
	excludeRPs []string,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute VMPowerCycleUptimeReport func.\n",
			time.Since(funcTimeStart),
		)
	}()

	rpNames := make([]string, len(rps))
	for i := range rps {
		rpNames[i] = rps[i].Name
	}

	var report strings.Builder

	fmt.Fprintf(
		&report,
		"VMs with high power cycle uptime:%s%s",
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
	)

	switch {
	case len(uptimeSummary.VMsCritical) > 0 || len(uptimeSummary.VMsWarning) > 0:

		vmsWithHighUptime := make(
			[]mo.VirtualMachine,
			0,
			len(uptimeSummary.VMsCritical)+len(uptimeSummary.VMsWarning),
		)

		vmsWithHighUptime = append(vmsWithHighUptime, uptimeSummary.VMsWarning...)
		vmsWithHighUptime = append(vmsWithHighUptime, uptimeSummary.VMsCritical...)

		sort.Slice(vmsWithHighUptime, func(i, j int) bool {
			return vmsWithHighUptime[i].Summary.QuickStats.UptimeSeconds > vmsWithHighUptime[j].Summary.QuickStats.UptimeSeconds
		})

		for _, vm := range vmsWithHighUptime {

			uptime := time.Duration(vm.Summary.QuickStats.UptimeSeconds) * time.Second
			uptimeDays := uptime.Hours() / 24

			fmt.Fprintf(
				&report,
				"* %s: %.2f days%s",
				vm.Name,
				uptimeDays,
				nagios.CheckOutputEOL,
			)
		}
	default:

		fmt.Fprintf(&report, "* None %s", nagios.CheckOutputEOL)

		fmt.Fprintf(
			&report,
			"%sTop 10 VMs, not yet exceeding power cycle uptime thresholds:%s%s",
			nagios.CheckOutputEOL,
			nagios.CheckOutputEOL,
			nagios.CheckOutputEOL,
		)

		sort.Slice(evaluatedVMs, func(i, j int) bool {
			return evaluatedVMs[i].Summary.QuickStats.UptimeSeconds > evaluatedVMs[j].Summary.QuickStats.UptimeSeconds
		})

		sampleStop := len(evaluatedVMs)
		if len(evaluatedVMs) > 10 {
			sampleStop = 10
		}
		for _, vm := range evaluatedVMs[:sampleStop] {

			uptime := time.Duration(vm.Summary.QuickStats.UptimeSeconds) * time.Second
			uptimeDays := uptime.Hours() / 24

			fmt.Fprintf(
				&report,
				"* %s: %.2f days%s",
				vm.Name,
				uptimeDays,
				nagios.CheckOutputEOL,
			)
		}

	}

	fmt.Fprintf(
		&report,
		"%s---%s%s",
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* vSphere environment: %s%s",
		c.URL().String(),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* VMs (evaluated: %d, total: %d)%s",
		len(evaluatedVMs),
		len(allVMs),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* Powered off VMs evaluated: %t%s",
		evalPoweredOffVMs,
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* Specified VMs to exclude (%d): [%v]%s",
		len(vmsToExclude),
		strings.Join(vmsToExclude, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* Specified Resource Pools to explicitly include (%d): [%v]%s",
		len(includeRPs),
		strings.Join(includeRPs, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* Specified Resource Pools to explicitly exclude (%d): [%v]%s",
		len(excludeRPs),
		strings.Join(excludeRPs, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		&report,
		"* Resource Pools evaluated (%d): [%v]%s",
		len(rpNames),
		strings.Join(rpNames, ", "),
		nagios.CheckOutputEOL,
	)

	return report.String()
}
