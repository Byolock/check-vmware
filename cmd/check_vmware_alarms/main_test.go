// Copyright 2021 Adam Chalkley
//
// https://github.com/atc0005/check-vmware
//
// Licensed under the MIT License. See LICENSE file in the project root for
// full license information.

package main

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/atc0005/check-vmware/internal/config"
	"github.com/atc0005/check-vmware/internal/vsphere"
	"github.com/google/go-cmp/cmp"
	"github.com/vmware/govmomi/vim25/types"
)

func getTestTriggeredAlarms() vsphere.TriggeredAlarms {

	return vsphere.TriggeredAlarms{

		// previously acknowledged (5 hours ago), triggered (24 hours ago)
		// yellow or WARNING datastore usage
		vsphere.TriggeredAlarm{
			Entity: vsphere.AlarmEntity{
				Name:          "RES-DC1-S6200-vol12",
				MOID:          types.ManagedObjectReference{Type: "Datastore", Value: "datastore-50120"},
				OverallStatus: types.ManagedEntityStatus("red"),
			},
			AcknowledgedTime:   time.Now().Add(-5 * time.Hour),
			Time:               time.Now().AddDate(0, 0, -1),
			Name:               "Datastore usage on disk",
			MOID:               types.ManagedObjectReference{Type: "Alarm", Value: "alarm-8"},
			Key:                "alarm-8.datastore-50120",
			Description:        "Default alarm to monitor datastore disk usage",
			Datacenter:         "Example",
			OverallStatus:      types.ManagedEntityStatus("yellow"),
			AcknowledgedByUser: "Ash",
			Acknowledged:       true,
		},

		// yellow or WARNING datastore usage
		vsphere.TriggeredAlarm{
			Entity: vsphere.AlarmEntity{
				Name:          "RES-DC1-S6200-vol11",
				MOID:          types.ManagedObjectReference{Type: "Datastore", Value: "datastore-50119"},
				OverallStatus: types.ManagedEntityStatus("yellow"),
			},
			AcknowledgedTime:   time.Time{},
			Time:               time.Now(),
			Name:               "Datastore usage on disk",
			MOID:               types.ManagedObjectReference{Type: "Alarm", Value: "alarm-8"},
			Key:                "alarm-8.datastore-50119",
			Description:        "Default alarm to monitor datastore disk usage",
			Datacenter:         "Example",
			OverallStatus:      types.ManagedEntityStatus("yellow"),
			AcknowledgedByUser: "",
			Acknowledged:       false,
		},

		// red or CRITICAL datastore usage
		vsphere.TriggeredAlarm{
			Entity: vsphere.AlarmEntity{
				Name:          "HUSVM-DC1-DigColl-vol8",
				MOID:          types.ManagedObjectReference{Type: "Datastore", Value: "datastore-141490"},
				OverallStatus: types.ManagedEntityStatus("red"),
			},
			AcknowledgedTime:   time.Time{},
			Time:               time.Now(),
			Name:               "Datastore usage on disk",
			MOID:               types.ManagedObjectReference{Type: "Alarm", Value: "alarm-8"},
			Key:                "alarm-8.datastore-141490",
			Description:        "Default alarm to monitor datastore disk usage",
			Datacenter:         "Example",
			OverallStatus:      types.ManagedEntityStatus("red"),
			AcknowledgedByUser: "",
			Acknowledged:       false,
		},

		// virtual machine CPU usage
		vsphere.TriggeredAlarm{
			Entity: vsphere.AlarmEntity{
				Name:          "node1.example.com",
				MOID:          types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-197"},
				OverallStatus: types.ManagedEntityStatus("red"),
			},
			AcknowledgedTime:   time.Time{},
			Time:               time.Now(),
			Name:               "Virtual machine CPU usage",
			MOID:               types.ManagedObjectReference{Type: "Alarm", Value: "alarm-6"},
			Key:                "alarm-6.vm-197",
			Description:        "Default alarm to monitor virtual machine CPU usage",
			Datacenter:         "Example",
			OverallStatus:      types.ManagedEntityStatus("red"),
			AcknowledgedByUser: "",
			Acknowledged:       false,
		},

		// virtual machine memory usage
		vsphere.TriggeredAlarm{
			Entity: vsphere.AlarmEntity{
				Name:          "node1.example.com",
				MOID:          types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-197"},
				OverallStatus: types.ManagedEntityStatus("red"),
			},
			AcknowledgedTime:   time.Time{},
			Time:               time.Now(),
			Name:               "Virtual machine memory usage",
			MOID:               types.ManagedObjectReference{Type: "Alarm", Value: "alarm-7"},
			Key:                "alarm-7.vm-197",
			Description:        "Default alarm to monitor virtual machine memory usage",
			Datacenter:         "Example",
			OverallStatus:      types.ManagedEntityStatus("red"),
			AcknowledgedByUser: "",
			Acknowledged:       false,
		},
	}
}

func TestFilters(t *testing.T) {

	if testing.Verbose() {
		t.Log("Enabling vsphere package logging output")
		vsphere.EnableLogging()
	}

	// setup table tests
	tests := []struct {

		// wantedNonExcludedAlarmKeysAfterFiltering is a collection of
		// triggered alarm keys (unique to a triggered alarm) which were *not*
		// filtered out.
		wantedNonExcludedAlarmKeysAfterFiltering []string

		// testName is the human readable name of the test case
		testName string

		// cfg is a copy of a configuration that models flag values provided
		// by a sysadmin. Highly variable.
		cfg config.Config

		// wantedNumTotalTriggeredAlarms is the expected number of total
		// triggered alarms. Usually the same number as the number returned by
		// `getTestTriggeredAlarms()`, unless the test case opts to "pretend"
		// that there are no triggered alarms available.
		wantedNumTotalTriggeredAlarms int

		// The desired number of excluded triggered alarms before filtering
		// takes place. Having *any* results for this value would be highly
		// unusual since triggered alarms are not filtered/excluded by
		// default.
		wantedNumExcludedAlarmsBeforeFiltering int

		// pretendNoAlarms indicates that this test case should act like there
		// are no triggered alarms within the monitored vSphere environment.
		pretendNoAlarms bool
	}{
		{
			testName: "Include VirtualMachine, Exclude VM CPU usage",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{"VirtualMachine"},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"Virtual machine CPU usage"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// explicitly included VirtualMachine triggered alarm matches
				// limited to memory usage due to CPU usage exclusion
				"alarm-7.vm-197",
			},
		},
		{
			testName: "Include VirtualMachine, Exclude VM CPU and memory usage",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{"VirtualMachine"},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"Virtual machine CPU usage", "memory usage"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:            5,
			wantedNumExcludedAlarmsBeforeFiltering:   0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// all explicitly included VirtualMachine matches excluded by
				// name
			},
		},
		{
			testName: "Pretend no alarms",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{"VirtualMachine"},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"Virtual machine CPU usage"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			pretendNoAlarms:                          true,
			wantedNumTotalTriggeredAlarms:            0,
			wantedNumExcludedAlarmsBeforeFiltering:   0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{},
		},
		{
			testName: "Exclude datastore usage, tacos on sale",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"datastore usage on disk", "tacos on sale"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// implicit VirtualMachine matches
				"alarm-6.vm-197",
				"alarm-7.vm-197",
			},
		},
		{
			testName: "Evaluate all",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: true,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				"alarm-8.datastore-50120",
				"alarm-8.datastore-50119",
				"alarm-8.datastore-141490",
				"alarm-6.vm-197",
				"alarm-7.vm-197",
			},
		},
		{
			testName: "Evaluate all unacknowledged",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				"alarm-8.datastore-50119",
				"alarm-8.datastore-141490",
				"alarm-6.vm-197",
				"alarm-7.vm-197",
			},
		},
		{
			testName: "Include Tacos on sale",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{"tacos on sale"},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:            5,
			wantedNumExcludedAlarmsBeforeFiltering:   0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// there are no tacos
			},
		},
		{
			testName: "Include Tacos on sale, evaluate acknowledged alarms",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{"tacos on sale"},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: true,
			},
			wantedNumTotalTriggeredAlarms:            5,
			wantedNumExcludedAlarmsBeforeFiltering:   0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// still no tacos
			},
		},
		{
			testName: "Include datastore usage",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{"datastore usage on disk"},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// two of three datastore triggered alarms; already
				// acknowledged alarm is ignored
				"alarm-8.datastore-50119",
				"alarm-8.datastore-141490",
			},
		},
		{
			testName: "Include datastore usage, eval previously acknowledged",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{"datastore usage on disk"},
				ExcludedAlarmNames:         []string{},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: true,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// all three datastore triggered alarms, including previously
				// acknowledged triggered alarm
				"alarm-8.datastore-50120",
				"alarm-8.datastore-50119",
				"alarm-8.datastore-141490",
			},
		},
		{
			testName: "Include VirtualMachine type, eval previously acknowledged, exclude VM CPU usage, include red status",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{"VirtualMachine"},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"Virtual machine CPU usage"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{"red"},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: true,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// explicitly included "VirtualMachine" entity type
				// explicitly excluded triggered alarm substring of "CPU usage"
				// explicitly included status of "red"
				//
				// pulls in all VirtualMachine triggered alarms
				// pulls in all triggered alarms with "red" status
				// excludes all triggered alarms with name substring of "Virtual machine CPU usage"
				"alarm-7.vm-197",
				"alarm-8.datastore-141490",
			},
		},
		{
			testName: "Include VirtualMachine type, exclude VM CPU usage, include yellow status",
			cfg: config.Config{
				Server:                     "vc1.example.com",
				Username:                   "vc1-read-only-service-account",
				Password:                   "placeholder",
				Domain:                     "example",
				LoggingLevel:               "info",
				DatacenterNames:            []string{"Example"},
				TrustCert:                  true,
				IncludedAlarmEntityTypes:   []string{"VirtualMachine"},
				ExcludedAlarmEntityTypes:   []string{},
				IncludedAlarmNames:         []string{},
				ExcludedAlarmNames:         []string{"Virtual machine CPU usage"},
				IncludedAlarmDescriptions:  []string{},
				ExcludedAlarmDescriptions:  []string{},
				IncludedAlarmStatuses:      []string{"yellow"},
				ExcludedAlarmStatuses:      []string{},
				EvaluateAcknowledgedAlarms: false,
			},
			wantedNumTotalTriggeredAlarms:          5,
			wantedNumExcludedAlarmsBeforeFiltering: 0,
			wantedNonExcludedAlarmKeysAfterFiltering: []string{
				// explicitly included "VirtualMachine" entity type
				// explicitly excluded triggered alarm substring of "CPU usage"
				// explicitly included status of "yellow"
				//
				// pulls in all VirtualMachine triggered alarms
				// pulls in all triggered alarms with "yellow" status
				// excludes all triggered alarms with name substring of "Virtual machine CPU usage"
				"alarm-7.vm-197",
				"alarm-8.datastore-50119",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {

			// initialize a fresh copy for every table test entry
			var triggeredAlarms vsphere.TriggeredAlarms
			if !tt.pretendNoAlarms {
				triggeredAlarms = getTestTriggeredAlarms()
			}

			// Pre-filtering checks
			numTriggeredAlarms := len(triggeredAlarms)
			switch {
			case numTriggeredAlarms != tt.wantedNumTotalTriggeredAlarms:
				t.Errorf(
					"want %d total triggered alarms before filtering; got %d",
					tt.wantedNumTotalTriggeredAlarms,
					numTriggeredAlarms,
				)
			default:
				t.Logf(
					"Got expected number (%d) of total triggered alarms before filtering",
					tt.wantedNumTotalTriggeredAlarms,
				)
			}

			numNonExcludedAlarmsBeforeFiltering := triggeredAlarms.NumExcluded()
			switch {
			case numNonExcludedAlarmsBeforeFiltering != tt.wantedNumExcludedAlarmsBeforeFiltering:
				t.Errorf(
					"want %d triggered alarms before filtering; got %d",
					tt.wantedNumExcludedAlarmsBeforeFiltering,
					numNonExcludedAlarmsBeforeFiltering,
				)
			default:
				t.Logf(
					"Got expected number (%d) of non-excluded alarms before filtering",
					tt.wantedNumExcludedAlarmsBeforeFiltering,
				)
			}

			switch {

			case len(triggeredAlarms) > 0:

				triggeredAlarmFilters := vsphere.TriggeredAlarmFilters{
					IncludedAlarmEntityTypes:   tt.cfg.IncludedAlarmEntityTypes,
					ExcludedAlarmEntityTypes:   tt.cfg.ExcludedAlarmEntityTypes,
					IncludedAlarmNames:         tt.cfg.IncludedAlarmNames,
					ExcludedAlarmNames:         tt.cfg.ExcludedAlarmNames,
					IncludedAlarmDescriptions:  tt.cfg.IncludedAlarmDescriptions,
					ExcludedAlarmDescriptions:  tt.cfg.ExcludedAlarmDescriptions,
					IncludedAlarmStatuses:      tt.cfg.IncludedAlarmStatuses,
					ExcludedAlarmStatuses:      tt.cfg.ExcludedAlarmStatuses,
					EvaluateAcknowledgedAlarms: tt.cfg.EvaluateAcknowledgedAlarms,
				}

				triggeredAlarms.Filter(triggeredAlarmFilters)

				//
				// Post-filtering
				//

				// Sort list of wanted/expected triggered alarm keys in the
				// same manner as TriggeredAlarms.Keys() method to aid in
				// comparing this collection against the collection that we
				// were left with after filtering.
				sort.Slice(tt.wantedNonExcludedAlarmKeysAfterFiltering, func(i, j int) bool {
					return strings.ToLower(tt.wantedNonExcludedAlarmKeysAfterFiltering[i]) <
						strings.ToLower(tt.wantedNonExcludedAlarmKeysAfterFiltering[j])
				})

				numTriggeredAlarmsToIgnore := triggeredAlarms.NumExcluded()
				numTriggeredAlarmsToReport := len(triggeredAlarms) - numTriggeredAlarmsToIgnore

				// gather all original triggered alarm keys
				// allTriggeredAlarmKeys := triggeredAlarms.Keys(true)

				// gather all non-filtered triggered alarm keys honoring the
				// test-specific choice of whether previously acknowledged
				// alarms should be evaluated.
				remainingTriggeredAlarmKeys := triggeredAlarms.Keys(tt.cfg.EvaluateAcknowledgedAlarms, false)

				t.Logf(
					"%d Triggered Alarms ignored: %v",
					numTriggeredAlarmsToIgnore,
					triggeredAlarms.KeysExcluded(),
				)
				t.Logf(
					"%d Triggered Alarms to report: %v",
					numTriggeredAlarmsToReport,
					remainingTriggeredAlarmKeys,
				)

				switch {
				case !cmp.Equal(tt.wantedNonExcludedAlarmKeysAfterFiltering, remainingTriggeredAlarmKeys):
					t.Errorf(
						"want %d triggered alarms after filtering; got %d",
						len(tt.wantedNonExcludedAlarmKeysAfterFiltering),
						len(remainingTriggeredAlarmKeys),
					)

					if d := cmp.Diff(remainingTriggeredAlarmKeys, tt.wantedNonExcludedAlarmKeysAfterFiltering); d != "" {
						t.Logf("(-got, +want)\n:%s", d)
					}

					var ctr int
					for _, ta := range triggeredAlarms {
						if !ta.Excluded() {
							ctr++
							// t.Logf("(%.2d) %+v\n", ctr, ta)
							t.Logf(
								"Alarm (%s) for %q of type %q with name %q not excluded",
								ta.OverallStatus,
								ta.Entity.Name,
								ta.Entity.MOID.Type,
								ta.Name,
							)
						}
					}
				default:
					t.Logf(
						"Got expected (%d) non-excluded alarms after filtering: %v",
						len(tt.wantedNonExcludedAlarmKeysAfterFiltering),
						tt.wantedNonExcludedAlarmKeysAfterFiltering,
					)

					var ctr int
					for _, ta := range triggeredAlarms {
						if !ta.Excluded() {
							ctr++
							// t.Logf("(%.2d) %+v\n", ctr, ta)
							t.Logf(
								"Alarm (%s) for %q of type %q with name %q not excluded",
								ta.OverallStatus,
								ta.Entity.Name,
								ta.Entity.MOID.Type,
								ta.Name,
							)
						}
					}

				}

				switch {
				case triggeredAlarms.HasCriticalState(false):
					t.Log("TriggeredAlarms have CRITICAL state")

				case triggeredAlarms.HasWarningState(false):
					t.Log("TriggeredAlarms have WARNING state")

				case triggeredAlarms.HasUnknownState(false):
					t.Log("TriggeredAlarms have UNKNOWN state")
				}

			default:

				t.Log("No non-excluded alarms detected")

			}
		})
	}

}
