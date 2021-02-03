package vsphere

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/atc0005/go-nagios"
	"github.com/vmware/govmomi/units"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// ErrSnapshotAgeThresholdCrossed indicates that a snapshot is older than a
// specified age threshold
var ErrSnapshotAgeThresholdCrossed = errors.New("snapshot exceeds specified age threshold")

// ErrSnapshotSizeThresholdCrossed indicates that a snapshot is larger than a
// specified size threshold
var ErrSnapshotSizeThresholdCrossed = errors.New("snapshot exceeds specified size threshold")

// ExceedsSize indicates whether a given snapshot size is greater than the
// specified value in GB.
func ExceedsSize(snapshotSize int64, thresholdSize int64) bool {
	return snapshotSize > (thresholdSize * units.GB)
}

// ExceedsAge indicates whether a given snapshot creation date is older than
// the specified number of days.
func ExceedsAge(snapshotCreated time.Time, days int) bool {

	now := time.Now()

	// Flip user specified number of days negative so that we can wind
	// back that many days from the file modification time. This gives
	// us our threshold to compare file modification times against.
	daysBack := -(days)
	ageThreshold := now.AddDate(0, 0, daysBack)

	switch {
	case snapshotCreated.Before(ageThreshold):
		return true
	case snapshotCreated.Equal(ageThreshold):
		return false
	case snapshotCreated.After(ageThreshold):
		return false

	// TODO: Is there any other state than Before, Equal and After?
	// TODO: Perhaps remove 'After' and use this instead?
	default:
		return false
	}

}

// FilterVMsWithSnapshots filters the provided collection of VirtualMachines
// to just those with snapshots. Later steps are responsible for validating
// whether those snapshots place the VMs into non-OK states.
func FilterVMsWithSnapshots(vms []mo.VirtualMachine) []mo.VirtualMachine {

	// setup early so we can reference it from deferred stats output
	var vmsWithSnapshots []mo.VirtualMachine

	funcTimeStart := time.Now()

	defer func(vms []mo.VirtualMachine, filteredVMs *[]mo.VirtualMachine) {
		logger.Printf(
			"It took %v to execute FilterVMsWithSnapshots func (for %d VMs, yielding %d VMs).\n",
			time.Since(funcTimeStart),
			len(vms),
			len(*filteredVMs),
		)
	}(vms, &vmsWithSnapshots)

	for _, vm := range vms {

		if vm.Snapshot != nil && vm.Snapshot.RootSnapshotList != nil {
			vmsWithSnapshots = append(vmsWithSnapshots, vm)
		}
	}

	return vmsWithSnapshots

}

// SnapshotSummary is intended to be a summary of the most commonly used
// snapshot details for a specific VirtualMachine snapshot.
type SnapshotSummary struct {

	// Name of the snapshot in human readable format.
	Name string

	// MOID is the Managed Object Reference value for the snapshot.
	MOID string

	// Description of the snapshot in human readable format.
	Description string

	// createTime is when the snapshot was created.
	createTime time.Time

	// Size is the size of the snapshot.
	Size int64

	// ID is the unique identifier that distinguishes this snapshot from other
	// snapshots of the virtual machine.
	ID int32

	// ageWarningState indicates whether this snapshot is considered in a
	// WARNING state based on crossing snapshot age threshold.
	ageWarningState bool

	// ageCriticalState indicates whether this snapshot is considered in a
	// CRITICAL state based on crossing snapshot age threshold.
	ageCriticalState bool

	// sizeWarningState indicates whether this snapshot is considered in a
	// WARNING state based on crossing snapshot size threshold.
	sizeWarningState bool

	// sizeCriticalState indicates whether this snapshot is considered in a
	// CRITICAL state based on crossing snapshot size threshold.
	sizeCriticalState bool

	VMName string
}

// SnapshotSummarySet ties a collection of snapshot summary values to a
// specific VirtualMachine by way of a VirtualMachine Managed Object
// Reference.
type SnapshotSummarySet struct {

	// VM is the Managed Object Reference for the VirtualMachine associated
	// with the snapshots in this set.
	VM types.ManagedObjectReference

	// VMName is the name of the VirtualMachine associated with the snapshots
	// in this set.
	VMName string

	// Snapshots is the collection of higher level summary values for
	// snapshots associated with a specific VirtualMachine.
	Snapshots []SnapshotSummary

	// setSizeWarningState indicates whether this snapshot set is considered in a
	// WARNING state based on cumulative size of all snapshots in the set
	// crossing snapshot size threshold.
	setSizeWarningState bool

	// setSizeCriticalState indicates whether this snapshot set is considered in a
	// WARNING state based on cumulative size of all snapshots in the set
	// crossing snapshot size threshold.
	setSizeCriticalState bool
}

// SnapshotSummarySets is a collection of SnapshotSummarySet types for bulk
// operations. Most often this is used when determining the overall state of
// all sets in the collection.
type SnapshotSummarySets []SnapshotSummarySet

// Size returns the size of all snapshots in the set.
func (sss SnapshotSummarySet) Size() int64 {
	var sum int64
	for i := range sss.Snapshots {
		sum += sss.Snapshots[i].Size
	}

	return sum
}

// SizeHR returns the human readable size of all snapshots in the set.
func (sss SnapshotSummarySet) SizeHR() string {
	return units.ByteSize(sss.Size()).String()
}

// ExceedsAge indicates how many snapshots in the set are older than the
// specified number of days. Unlike the ExceedsAge method for
// SnapshotSummarySets, this method focuses specifically on individual
// snapshots.
func (sss SnapshotSummarySet) ExceedsAge(days int) int {

	var numExceeded int
	for _, snap := range sss.Snapshots {
		if snap.IsAgeExceeded(days) {
			numExceeded++
		}
	}

	return numExceeded
}

// ExceedsSize indicates how many snapshots in the set are larger than the
// specified size in GB. Unlike the ExceedsSize method for
// SnapshotSummarySets, this method focuses specifically on individual
// snapshot size.
func (sss SnapshotSummarySet) ExceedsSize(sizeGB int) int {

	var numSnapshotsExceeded int
	for _, snap := range sss.Snapshots {
		if snap.IsSizeExceeded(sizeGB) {
			numSnapshotsExceeded++
		}
	}

	return numSnapshotsExceeded
}

// Snapshots indicates how many snapshots are in all of the sets.
func (sss SnapshotSummarySets) Snapshots() int {

	var numSnapshots int
	for _, set := range sss {
		numSnapshots += len(set.Snapshots)
	}

	return numSnapshots
}

// ExceedsAge indicates how many sets and number of snapshots from all of
// those sets are older than the specified number of days.
func (sss SnapshotSummarySets) ExceedsAge(days int) (int, int) {

	var setsExceeded int
	var snapshotsExceeded int
	for _, set := range sss {
		if set.ExceedsAge(days) >= 1 {
			setsExceeded++
			snapshotsExceeded += set.ExceedsAge(days)
		}
	}

	return setsExceeded, snapshotsExceeded
}

// ExceedsSize indicates how many sets and number of snapshots from all of
// those sets have cumulative snapshots larger than the specified size in GB.
func (sss SnapshotSummarySets) ExceedsSize(sizeGB int) (int, int) {

	var numSetsExceeded int
	var numSnapshotsExceeded int
	for _, set := range sss {
		if set.Size() > (int64(sizeGB) * units.GB) {
			numSetsExceeded++
			numSnapshotsExceeded += len(set.Snapshots)
		}
	}

	return numSetsExceeded, numSnapshotsExceeded
}

// HasNotYetExceededAge indicates whether any of the snapshots in any of the
// sets have yet to exceed the threshold for the specified number of days.
func (sss SnapshotSummarySets) HasNotYetExceededAge(days int) bool {

	for _, set := range sss {
		for _, snapSummary := range set.Snapshots {
			if !ExceedsAge(snapSummary.createTime, days) {
				return true
			}
		}
	}

	return false
}

// HasNotYetExceededSize indicates whether any snapshot set (all snapshots for
// a specific VM) has yet to exceed the threshold for the specified size in
// GB.
func (sss SnapshotSummarySets) HasNotYetExceededSize(sizeGB int) bool {

	for _, set := range sss {
		if !ExceedsSize(set.Size(), int64(sizeGB)) {
			return true
		}
	}

	return false
}

// SizeHR returns the human readable size of the snapshot.
func (ss SnapshotSummary) SizeHR() string {
	return units.ByteSize(ss.Size).String()
}

// AgeDays returns the age of a snapshot in days.
func (ss SnapshotSummary) AgeDays() float64 {

	now := time.Now()

	return now.Sub(ss.createTime).Hours() / 24

}

// Age returns the age of a snapshot in formatted days.
func (ss SnapshotSummary) Age() string {

	return fmt.Sprintf(
		"%.2f days",
		ss.AgeDays(),
	)

}

// IsAgeExceeded indicates whether the snapshot is older than the specified
// number of days.
func (ss SnapshotSummary) IsAgeExceeded(days int) bool {
	return ExceedsAge(ss.createTime, days)
}

// IsSizeExceeded indicates whether the snapshot is larger than the specified
// size in GB.
func (ss SnapshotSummary) IsSizeExceeded(sizeGB int) bool {
	return ExceedsSize(ss.Size, int64(sizeGB))
}

// IsWarningState indicates whether the snapshot has exceeded age or size
// WARNING thresholds.
func (ss SnapshotSummary) IsWarningState() bool {
	return ss.ageWarningState || ss.sizeWarningState
}

// IsCriticalState indicates whether the snapshot has exceeded age or size
// CRITICAL thresholds.
func (ss SnapshotSummary) IsCriticalState() bool {
	return ss.ageCriticalState || ss.sizeCriticalState
}

// IsAgeWarningState indicates whether the snapshot has exceeded the age
// WARNING threshold.
func (ss SnapshotSummary) IsAgeWarningState() bool {
	return ss.ageWarningState
}

// IsAgeCriticalState indicates whether the snapshot has exceeded the age
// CRITICAL threshold.
func (ss SnapshotSummary) IsAgeCriticalState() bool {
	return ss.ageCriticalState
}

// IsSizeWarningState indicates whether the snapshot has exceeded the size
// WARNING threshold.
func (ss SnapshotSummary) IsSizeWarningState() bool {
	return ss.sizeWarningState
}

// IsSizeCriticalState indicates whether the snapshot has exceeded the size
// CRITICAL threshold.
func (ss SnapshotSummary) IsSizeCriticalState() bool {
	return ss.sizeCriticalState
}

// IsWarningState indicates whether the snapshot set has exceeded age or size
// WARNING thresholds.
func (sss SnapshotSummarySet) IsWarningState() bool {
	for i := range sss.Snapshots {
		if sss.Snapshots[i].IsWarningState() {
			return true
		}
	}

	return false
}

// IsCriticalState indicates whether the snapshot set has exceeded age or size
// CRITICAL thresholds.
func (sss SnapshotSummarySet) IsCriticalState() bool {
	for i := range sss.Snapshots {
		if sss.Snapshots[i].IsCriticalState() {
			return true
		}
	}

	return false
}

// IsAgeWarningState indicates whether the snapshot set has exceeded the age
// WARNING threshold.
func (sss SnapshotSummarySet) IsAgeWarningState() bool {
	for i := range sss.Snapshots {
		if sss.Snapshots[i].IsAgeWarningState() {
			return true
		}
	}

	return false
}

// IsAgeCriticalState indicates whether the snapshot set has exceeded the age
// CRITICAL threshold.
func (sss SnapshotSummarySet) IsAgeCriticalState() bool {
	for i := range sss.Snapshots {
		if sss.Snapshots[i].IsAgeCriticalState() {
			return true
		}
	}

	return false
}

// IsSizeWarningState indicates whether the snapshot set has exceeded the
// size WARNING threshold.
func (sss SnapshotSummarySet) IsSizeWarningState() bool {
	return sss.setSizeWarningState
}

// IsSizeCriticalState indicates whether the snapshot set has exceeded the
// size CRITICAL threshold.
func (sss SnapshotSummarySet) IsSizeCriticalState() bool {
	return sss.setSizeCriticalState
}

// IsWarningState indicates whether the snapshot sets have exceeded age or
// size WARNING thresholds.
func (sss SnapshotSummarySets) IsWarningState() bool {
	for i := range sss {
		if sss[i].IsWarningState() {
			return true
		}
	}

	return false
}

// IsCriticalState indicates whether the snapshot sets have exceeded age or
// size CRITICAL thresholds.
func (sss SnapshotSummarySets) IsCriticalState() bool {
	for i := range sss {
		if sss[i].IsCriticalState() {
			return true
		}
	}

	return false
}

// IsAgeWarningState indicates whether the snapshot sets have exceeded the age
// WARNING threshold.
func (sss SnapshotSummarySets) IsAgeWarningState() bool {
	for i := range sss {
		if sss[i].IsAgeWarningState() {
			return true
		}
	}

	return false
}

// IsAgeCriticalState indicates whether the snapshot sets have exceeded the
// age CRITICAL threshold.
func (sss SnapshotSummarySets) IsAgeCriticalState() bool {
	for i := range sss {
		if sss[i].IsAgeCriticalState() {
			return true
		}
	}

	return false
}

// IsSizeWarningState indicates whether the snapshot sets have exceeded the
// size WARNING threshold.
func (sss SnapshotSummarySets) IsSizeWarningState() bool {
	for i := range sss {
		if sss[i].IsSizeWarningState() {
			return true
		}
	}

	return false
}

// IsSizeCriticalState indicates whether the snapshot sets have exceeded the
// size CRITICAL threshold.
func (sss SnapshotSummarySets) IsSizeCriticalState() bool {
	for i := range sss {
		if sss[i].IsSizeCriticalState() {
			return true
		}
	}

	return false
}

// SnapshotsIndex is a mapping of Snapshot ManagedObjectReference to a tree of
// snapshot details. This type is intended to help with producing a superset
// type combining a summary of snapshot metadata with the original
// VirtualMachine object.
//
// Deprecated ?
type SnapshotsIndex map[string]types.VirtualMachineSnapshotTree

// removeFileKey removes a given file key directly from the list of file keys
func removeFileKey(l *[]int32, key int32) {
	for i, k := range *l {
		if k == key {
			*l = append((*l)[:i], (*l)[i+1:]...)
			break
		}
	}
}

// ListVMSnapshots generates a quick listing of all snapshots for a given VM
// and emits the results to the provided io.Writer.
func ListVMSnapshots(vm mo.VirtualMachine, w io.Writer) {

	now := time.Now()

	var listFunc func(mo.VirtualMachine, []types.VirtualMachineSnapshotTree, *types.ManagedObjectReference)

	listFunc = func(vm mo.VirtualMachine, snapTrees []types.VirtualMachineSnapshotTree, parent *types.ManagedObjectReference) {

		if len(snapTrees) == 0 {
			return
		}

		for _, snapTree := range snapTrees {

			daysAge := now.Sub(snapTree.CreateTime).Hours() / 24

			fmt.Fprintf(
				w,
				"Snapshot [Name: %v, Age: %v, ID: %v, MOID: %v, Active: %t]\n",
				snapTree.Name,
				// snapTree.CreateTime.Format("2006-01-02 15:04:05"),
				daysAge,
				snapTree.Id,
				snapTree.Snapshot.Value,
				snapTree.Snapshot.Value == vm.Snapshot.CurrentSnapshot.Value,
			)

			if snapTree.ChildSnapshotList != nil {
				listFunc(vm, snapTree.ChildSnapshotList, &snapTree.Snapshot)
			}

		}
	}

	listFunc(vm, vm.Snapshot.RootSnapshotList, nil)

}

// NewSnapshotSummarySet returns a set of SnapshotSummary values for snapshots
// associated with a specified VirtualMachine.
func NewSnapshotSummarySet(
	vm mo.VirtualMachine,
	snapshotsAgeCritical int,
	snapshotsAgeWarning int,
	snapshotsSizeCritical int,
	snapshotsSizeWarning int,
) SnapshotSummarySet {

	// TODO: Return error if no snapshots are present?

	logger.Println("Number of snapshot trees:", len(vm.Snapshot.RootSnapshotList))
	if vm.Snapshot.CurrentSnapshot != nil {
		logger.Println("Active snapshot MOID:", vm.Snapshot.CurrentSnapshot)
	}

	funcTimeStart := time.Now()

	var snapshots []SnapshotSummary

	defer func(ss *[]SnapshotSummary) {
		logger.Printf(
			"It took %v to execute NewSnapshotSummarySet func "+
				"(and retrieve %d snapshot summaries).\n",
			time.Since(funcTimeStart),
			len(*ss),
		)
	}(&snapshots)

	// all disk files attached to the virtual machine at the current point of
	// running
	vmAllDiskFileKeys := make([]int32, 0, len(vm.LayoutEx.Disk)*2)
	for _, layoutExDisk := range vm.LayoutEx.Disk {
		for _, link := range layoutExDisk.Chain {
			vmAllDiskFileKeys = append(vmAllDiskFileKeys, link.FileKey...)
		}
	}

	logger.Printf("vmAllDiskFileKeys (%d): %v\n", len(vmAllDiskFileKeys), vmAllDiskFileKeys)

	// all files (vm.LayoutEx.File) attached to the virtual machine, indexed
	// by file key (vm.LayoutEx.File.Key) to make retrieving the size for a
	// specific file easier later
	fileKeyMap := make(map[int32]types.VirtualMachineFileLayoutExFileInfo)
	logger.Printf("Disk files (diskDescriptor, diskExtent) attached for Virtual Machine's current state:")
	for _, fileLayout := range vm.LayoutEx.File {

		fileKeyMap[fileLayout.Key] = fileLayout

		// list disk files only
		if fileLayout.Type == "diskDescriptor" || fileLayout.Type == "diskExtent" {
			logger.Printf(
				"* fileLayout [Name: %v, Size: %v (%s), Key: %v]\n",
				fileLayout.Name,
				fileLayout.Size,
				units.ByteSize(fileLayout.Size),
				fileLayout.Key,
			)
		}
	}

	var crawlFunc func(mo.VirtualMachine, []types.VirtualMachineSnapshotTree, *types.ManagedObjectReference)

	crawlFunc = func(vm mo.VirtualMachine, snapTrees []types.VirtualMachineSnapshotTree, parent *types.ManagedObjectReference) {

		if len(snapTrees) == 0 {
			return
		}

		for _, snapTree := range snapTrees {

			logger.Printf(
				"Processing snapshot: [ID: %s, Name: %s, HasParent: %t]\n",
				snapTree.Snapshot.Value,
				snapTree.Name,
				parent != nil,
			)

			logger.Printf(
				"Active snapshot: %s\n",
				vm.Snapshot.CurrentSnapshot.Value,
			)

			var snapshotSize int64

			parentSnapshotDiskFileKeys := make([]int32, 0, len(vmAllDiskFileKeys))
			snapshotDiskFileKeys := make([]int32, 0, len(vmAllDiskFileKeys))

			logger.Printf("Collecting snapshot disk, data file keys ...")
			for _, snapLayout := range vm.LayoutEx.Snapshot {

				// Evaluating snapshot layout for current snapshot tree.
				if snapLayout.Key.Value == snapTree.Snapshot.Value {

					logger.Println(
						"Adding snapTree (vmsn, snapData) file key",
						snapLayout.DataKey,
					)
					logger.Printf(
						"snapLayout [Name: %v, Size: %v (%s), Key: %v]\n",
						fileKeyMap[snapLayout.DataKey].Name,
						fileKeyMap[snapLayout.DataKey].Size,
						units.ByteSize(fileKeyMap[snapLayout.DataKey].Size),
						snapLayout.DataKey,
					)
					snapshotDiskFileKeys = append(snapshotDiskFileKeys, snapLayout.DataKey)

					// Grab all disk file keys for the snapshot tree we are
					// currently evaluating.
					for _, snapLayoutExDisk := range snapLayout.Disk {
						for _, link := range snapLayoutExDisk.Chain {
							logger.Println("Adding snapTree disk descriptor, extent file keys", link.FileKey)
							snapshotDiskFileKeys = append(snapshotDiskFileKeys, link.FileKey...)
						}
					}
				}

				// Fetch disk keys for parent snapshot, if present
				if parent != nil && snapLayout.Key.Value == parent.Value {
					for _, snapLayoutExDisk := range snapLayout.Disk {
						for _, link := range snapLayoutExDisk.Chain {
							logger.Println("Adding parent disk descriptor, extent keys", link.FileKey)
							parentSnapshotDiskFileKeys = append(parentSnapshotDiskFileKeys, link.FileKey...)
						}
					}
				}
			}

			// Retain a copy of all snapshot keys for later use
			allSnapshotKeys := make([]int32, len(snapshotDiskFileKeys))
			copy(allSnapshotKeys, snapshotDiskFileKeys)

			// TODO: Is it cheaper to copy vmAllDiskFileKeys here for per-loop
			// iteration use, or move the creation of vmAllDiskFileKeys list
			// inside the loop in order to drop the use of an extra variable?
			remainingDiskFiles := make([]int32, len(vmAllDiskFileKeys))
			copy(remainingDiskFiles, vmAllDiskFileKeys)

			// logger.Printf("Current snapshotDiskFileKeys:", snapshotDiskFileKeys)
			// logger.Printf("Current allSnapshotKeys:", allSnapshotKeys)
			// logger.Printf("")
			// logger.Printf("Current vmAllDiskFileKeys:", vmAllDiskFileKeys)
			// logger.Printf("Current remainingDiskFiles:", remainingDiskFiles)

			// Conditionally prune disk files not directly associated with the
			// unique snapshot tree we are evaluating
			switch {

			case parent == nil:

				// No parent snapshot is present. Remove all attached disk
				// file keys from the list of snapshot file keys. This leaves
				// the snapshot data file as the sole file key in the list.

				logger.Printf("Removing file keys for attached VM disks from list for current snapshot tree ...")

				for _, key := range vmAllDiskFileKeys {
					logger.Printf("Removing key %d\n", key)
					removeFileKey(&snapshotDiskFileKeys, key)
				}

			case parent != nil:

				// Parent snapshot is present. Remove all parent snapshot file
				// keys from the list of snapshot file keys. This leaves only
				// the snapshot file keys associated with the fixed snapshot
				// state.

				logger.Printf(
					"Removing parent snapshot disk file keys from list for current snapshot tree ...",
				)
				for _, key := range parentSnapshotDiskFileKeys {
					logger.Printf("Removing key %d\n", key)
					removeFileKey(&snapshotDiskFileKeys, key)

				}

			}

			logger.Println(
				"Remaining file keys in list for current snapshot tree:",
				snapshotDiskFileKeys,
			)
			logger.Printf("Computing snapshot size (using remaining snapshot tree file keys)")
			for _, fileKey := range snapshotDiskFileKeys {
				snapshotSize += fileKeyMap[fileKey].Size
			}

			// If the current snapshot tree we are evaluating is active,
			// include additional disk files not associated with a parent
			// snapshot or the current snapshot in size calculations. This
			// allows for measuring and including the growth from the last
			// fixed snapshot to the present state.
			if snapTree.Snapshot.Value == vm.Snapshot.CurrentSnapshot.Value {
				logger.Println("allSnapshotKeys:", allSnapshotKeys)
				for _, fileKey := range allSnapshotKeys {
					removeFileKey(&remainingDiskFiles, fileKey)
				}
				logger.Println("remainingDiskFiles:", remainingDiskFiles)
				logger.Println("Updating computed snapshot size (using keys from remainingDiskFiles)")
				for _, fileKey := range remainingDiskFiles {
					snapshotSize += fileKeyMap[fileKey].Size
				}
			}

			logger.Printf(
				"Size [bytes: %v, HR: %s] calculated for %s snapshot\n\n\n",
				snapshotSize,
				units.ByteSize(snapshotSize),
				snapTree.Name,
			)

			snapshots = append(snapshots, SnapshotSummary{
				Name:              snapTree.Name,
				VMName:            vm.Name,
				ID:                snapTree.Id,
				MOID:              snapTree.Snapshot.Value,
				Description:       snapTree.Description,
				Size:              snapshotSize,
				createTime:        snapTree.CreateTime,
				ageWarningState:   ExceedsAge(snapTree.CreateTime, snapshotsAgeWarning),
				ageCriticalState:  ExceedsAge(snapTree.CreateTime, snapshotsAgeCritical),
				sizeWarningState:  ExceedsSize(snapshotSize, int64(snapshotsSizeCritical)),
				sizeCriticalState: ExceedsSize(snapshotSize, int64(snapshotsSizeWarning)),
			})

			if snapTree.ChildSnapshotList != nil {
				crawlFunc(vm, snapTree.ChildSnapshotList, &snapTree.Snapshot)
			}

		}
	}

	// no parent to pass in for the root
	crawlFunc(vm, vm.Snapshot.RootSnapshotList, nil)

	var setSize int64
	for _, snap := range snapshots {
		setSize += snap.Size
	}

	logger.Println("setSize for VM ", vm.Name, ":", setSize)
	logger.Println("setSizeWarningState for VM ", vm.Name, ":", ExceedsSize(setSize, int64(snapshotsSizeWarning)))
	logger.Println("setSizeCriticalState for VM ", vm.Name, ":", ExceedsSize(setSize, int64(snapshotsSizeCritical)))

	return SnapshotSummarySet{
		VM:                   vm.Self,
		VMName:               vm.Name,
		Snapshots:            snapshots,
		setSizeWarningState:  ExceedsSize(setSize, int64(snapshotsSizeWarning)),
		setSizeCriticalState: ExceedsSize(setSize, int64(snapshotsSizeCritical)),
	}

}

// SnapshotsAgeOneLineCheckSummary is used to generate a one-line Nagios
// service check results summary. This is the line most prominent in
// notifications.
func SnapshotsAgeOneLineCheckSummary(
	stateLabel string,
	snapshotSets SnapshotSummarySets,
	snapshotsAgeCritical int,
	snapshotsAgeWarning int,
	evaluatedVMs []mo.VirtualMachine,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute SnapshotsAgeOneLineCheckSummary func.\n",
			time.Since(funcTimeStart),
		)
	}()

	switch {

	case snapshotSets.IsAgeCriticalState():

		vms, snapshots := snapshotSets.ExceedsAge(snapshotsAgeCritical)

		return fmt.Sprintf(
			"%s: %d VMs with %d snapshots older than %d days detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			vms,
			snapshots,
			snapshotsAgeCritical,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	case snapshotSets.IsAgeWarningState():

		vms, snapshots := snapshotSets.ExceedsAge(snapshotsAgeWarning)

		return fmt.Sprintf(
			"%s: %d VMs with %d snapshots older than %d days detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			vms,
			snapshots,
			snapshotsAgeWarning,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	default:

		return fmt.Sprintf(
			"%s: No snapshots older than %d days detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			snapshotsAgeWarning,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	}
}

// SnapshotsSizeOneLineCheckSummary is used to generate a one-line Nagios
// service check results summary. This is the line most prominent in
// notifications.
func SnapshotsSizeOneLineCheckSummary(
	stateLabel string,
	snapshotSets SnapshotSummarySets,
	snapshotsSizeCritical int,
	snapshotsSizeWarning int,
	evaluatedVMs []mo.VirtualMachine,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute SnapshotsSizeOneLineCheckSummary func.\n",
			time.Since(funcTimeStart),
		)
	}()

	switch {

	case snapshotSets.IsSizeCriticalState():

		vms, snapshots := snapshotSets.ExceedsSize(snapshotsSizeCritical)
		return fmt.Sprintf(
			"%s: %d VMs with combined snapshots (%d) exceeding %d %s detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			vms,
			snapshots,
			snapshotsSizeCritical,
			snapshotThresholdTypeSizeSuffix,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	case snapshotSets.IsSizeWarningState():

		vms, snapshots := snapshotSets.ExceedsSize(snapshotsSizeWarning)

		return fmt.Sprintf(
			"%s: %d VMs with combined snapshots (%d) exceeding %d %s detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			vms,
			snapshots,
			snapshotsSizeWarning,
			snapshotThresholdTypeSizeSuffix,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	default:

		return fmt.Sprintf(
			"%s: No VMs, each with combined snapshots exceeding %d %s detected (evaluated %d VMs, %d Snapshots, %d Resource Pools)",
			stateLabel,
			snapshotsSizeWarning,
			snapshotThresholdTypeSizeSuffix,
			len(evaluatedVMs),
			snapshotSets.Snapshots(),
			len(rps),
		)

	}
}

// writeSnapshotsListEntries generates a common snapshots report for both age
// and size checks listing any snapshots which have exceeded thresholds along
// with any snapshots which have not yet exceeded them.
func writeSnapshotsListEntries(
	w io.Writer,
	snapshotCriticalThreshold int,
	snapshotWarningThreshold int,
	unitSuffix string,
	unitName string,
	snapshotSummarySets SnapshotSummarySets,
) {

	listEntryTemplate := "* %q [Age: %v, Size (item: %v, sum: %v), Name: %q, ID: %v]\n"

	fmt.Fprintf(
		w,
		"Snapshots exceeding WARNING (%d%s) or CRITICAL (%d%s) %s thresholds:%s%s",
		snapshotWarningThreshold,
		unitSuffix,
		snapshotCriticalThreshold,
		unitSuffix,
		unitName,
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
	)

	switch {

	case unitName == snapshotThresholdTypeAge &&
		(snapshotSummarySets.IsAgeCriticalState() ||
			snapshotSummarySets.IsAgeWarningState()):
		for _, snapSet := range snapshotSummarySets {
			for _, snap := range snapSet.Snapshots {
				if snap.IsAgeCriticalState() || snap.IsAgeWarningState() {
					fmt.Fprintf(
						w,
						listEntryTemplate,
						snap.VMName,
						snap.Age(),
						snap.SizeHR(),
						snapSet.SizeHR(),
						snap.Name,
						snap.MOID,
					)
				}
			}
		}

	case unitName == snapshotThresholdTypeSize &&
		(snapshotSummarySets.IsSizeCriticalState() ||
			snapshotSummarySets.IsSizeWarningState()):
		for _, snapSet := range snapshotSummarySets {
			if snapSet.IsSizeWarningState() || snapSet.IsSizeCriticalState() {
				for _, snap := range snapSet.Snapshots {
					fmt.Fprintf(
						w,
						listEntryTemplate,
						snap.VMName,
						snap.Age(),
						snap.SizeHR(),
						snapSet.SizeHR(),
						snap.Name,
						snap.MOID,
					)
				}
			}
		}

	default:
		fmt.Fprintln(w, "* None detected")
	}

	fmt.Fprintf(
		w,
		"%sSnapshots *not yet* exceeding %s thresholds:%s%s",
		nagios.CheckOutputEOL,
		unitName,
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
	)

	switch {

	case unitName == snapshotThresholdTypeAge &&
		snapshotSummarySets.HasNotYetExceededAge(snapshotWarningThreshold):
		for _, snapSet := range snapshotSummarySets {
			for _, snap := range snapSet.Snapshots {
				if !(snap.IsAgeCriticalState() ||
					snap.IsAgeWarningState()) {
					fmt.Fprintf(
						w,
						listEntryTemplate,
						snap.VMName,
						snap.Age(),
						snap.SizeHR(),
						snapSet.SizeHR(),
						snap.Name,
						snap.MOID,
					)
				}
			}
		}

	case unitName == snapshotThresholdTypeSize &&
		snapshotSummarySets.HasNotYetExceededSize(snapshotWarningThreshold):
		for _, snapSet := range snapshotSummarySets {
			if !(snapSet.IsSizeWarningState() ||
				snapSet.IsSizeCriticalState()) {
				for _, snap := range snapSet.Snapshots {
					fmt.Fprintf(
						w,
						listEntryTemplate,
						snap.VMName,
						snap.Age(),
						snap.SizeHR(),
						snapSet.SizeHR(),
						snap.Name,
						snap.MOID,
					)
				}
			}
		}

	default:
		fmt.Fprintln(w, "* None detected")
	}

}

// writeSnapshotsReportFooter generates a common "footer" for use with
// summarizing snapshots age and size plugin check results.
//
// TODO: Refactor for shared use by other (all?) plugins
func writeSnapshotsReportFooter(
	c *vim25.Client,
	w io.Writer,
	allVMs []mo.VirtualMachine,
	evaluatedVMs []mo.VirtualMachine,
	vmsWithIssues []mo.VirtualMachine,
	vmsToExclude []string,
	evalPoweredOffVMs bool,
	includeRPs []string,
	excludeRPs []string,
	rps []mo.ResourcePool,
) {

	rpNames := make([]string, len(rps))
	for i := range rps {
		rpNames[i] = rps[i].Name
	}

	fmt.Fprintf(
		w,
		"%s---%s%s",
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* vSphere environment: %s%s",
		c.URL().String(),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* VMs (evaluated: %d, total: %d)%s",
		len(evaluatedVMs),
		len(allVMs),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* Powered off VMs evaluated: %t%s",
		// NOTE: This plugin is hard-coded to evaluate powered off and powered
		// on VMs equally. I'm not sure whether ignoring powered off VMs by
		// default makes sense for this particular plugin.
		//
		// Please share your feedback on this GitHub issue if you feel differently:
		// https://github.com/atc0005/check-vmware/issues/79
		//
		// Please expand on some use cases for ignoring powered off VMs by default.
		true,
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* Specified VMs to exclude (%d): [%v]%s",
		len(vmsToExclude),
		strings.Join(vmsToExclude, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* Specified Resource Pools to explicitly include (%d): [%v]%s",
		len(includeRPs),
		strings.Join(includeRPs, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* Specified Resource Pools to explicitly exclude (%d): [%v]%s",
		len(excludeRPs),
		strings.Join(excludeRPs, ", "),
		nagios.CheckOutputEOL,
	)

	fmt.Fprintf(
		w,
		"* Resource Pools evaluated (%d): [%v]%s",
		len(rpNames),
		strings.Join(rpNames, ", "),
		nagios.CheckOutputEOL,
	)

}

// SnapshotsAgeReport generates a summary of snapshot details along with
// various verbose details intended to aid in troubleshooting check results at
// a glance. This information is provided for use with the Long Service Output
// field commonly displayed on the detailed service check results display in
// the web UI or in the body of many notifications.
func SnapshotsAgeReport(
	c *vim25.Client,
	snapshotSummarySets SnapshotSummarySets,
	snapshotsAgeCritical int,
	snapshotsAgeWarning int,
	allVMs []mo.VirtualMachine,
	evaluatedVMs []mo.VirtualMachine,
	vmsWithIssues []mo.VirtualMachine,
	vmsToExclude []string,
	evalPoweredOffVMs bool,
	includeRPs []string,
	excludeRPs []string,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute SnapshotsAgeReport func.\n",
			time.Since(funcTimeStart),
		)
	}()

	var report strings.Builder

	writeSnapshotsListEntries(
		&report,
		snapshotsAgeCritical,
		snapshotsAgeWarning,
		snapshotThresholdTypeAgeSuffix,
		snapshotThresholdTypeAge,
		snapshotSummarySets,
	)

	// Generate common footer information, send to strings Builder
	writeSnapshotsReportFooter(
		c,
		&report,
		allVMs,
		evaluatedVMs,
		vmsWithIssues,
		vmsToExclude,
		evalPoweredOffVMs,
		includeRPs,
		excludeRPs,
		rps,
	)

	return report.String()
}

// SnapshotsSizeReport generates a summary of snapshot details along with
// various verbose details intended to aid in troubleshooting check results at
// a glance. This information is provided for use with the Long Service Output
// field commonly displayed on the detailed service check results display in
// the web UI or in the body of many notifications.
func SnapshotsSizeReport(
	c *vim25.Client,
	snapshotSummarySets SnapshotSummarySets,
	snapshotsSizeCritical int,
	snapshotsSizeWarning int,
	allVMs []mo.VirtualMachine,
	evaluatedVMs []mo.VirtualMachine,
	vmsWithIssues []mo.VirtualMachine,
	vmsToExclude []string,
	evalPoweredOffVMs bool,
	includeRPs []string,
	excludeRPs []string,
	rps []mo.ResourcePool,
) string {

	funcTimeStart := time.Now()

	defer func() {
		logger.Printf(
			"It took %v to execute SnapshotsSizeReport func.\n",
			time.Since(funcTimeStart),
		)
	}()

	var report strings.Builder

	writeSnapshotsListEntries(
		&report,
		snapshotsSizeCritical,
		snapshotsSizeWarning,
		snapshotThresholdTypeSizeSuffix,
		snapshotThresholdTypeSize,
		snapshotSummarySets,
	)

	// Generate common footer information, send to strings Builder
	writeSnapshotsReportFooter(
		c,
		&report,
		allVMs,
		evaluatedVMs,
		vmsWithIssues,
		vmsToExclude,
		evalPoweredOffVMs,
		includeRPs,
		excludeRPs,
		rps,
	)

	return report.String()
}
