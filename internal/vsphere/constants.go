// Copyright 2021 Adam Chalkley
//
// https://github.com/atc0005/check-vmware
//
// Licensed under the MIT License. See LICENSE file in the project root for
// full license information.

package vsphere

// ParentResourcePool represents the hidden resource pool named Resources
// which is present on virtual machine hosts. This resource pool is a parent
// of all resource pools of the host. Including this pool in "eligible"
// resource pool lists throws off calculations (e.g., causes a VM to show up
// twice).
const ParentResourcePool string = "Resources"

const failedToUseFailedToFallback string = "error: failed to use provided datacenter, failed to fallback to default datacenter"

const dcNotProvidedFailedToFallback string = "error: datacenter not provided, failed to fallback to default datacenter"