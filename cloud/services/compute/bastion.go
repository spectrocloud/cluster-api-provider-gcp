/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package compute

import (
	"fmt"
	"github.com/pkg/errors"
	"google.golang.org/api/compute/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1alpha2"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/gcperrors"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/wait"
)

// ReconcileBastion ensures a bastion is created for the cluster
func (s *Service) ReconcileBastion() error {
	network, err := s.networks.Get(s.scope.Project(), s.scope.NetworkName()).Do()
	if gcperrors.IsNotFound(err) {
		return nil
	}

	// Return early if the description doesn't match our ownership tag.
	if network.Description != infrav1.ClusterTagKey(s.scope.Name()) {
		return nil
	}

	s.scope.V(2).Info("Reconciling bastion host")

	// Describe bastion instance, if any.
	instance, err := s.describeBastionInstance()
	if gcperrors.IsNotFound(err) {
		spec := s.getDefaultBastion()
		instance, err = s.runInstance(spec)
		if err != nil {
			///record.Warnf(s.scope.GCPCluster, "FailedCreateBastion", "Failed to create bastion instance: %v", err)
			return err
		}

		//record.Eventf(s.scope.GCPCluster, "SuccessfulCreateBastion", "Created bastion instance %q", instance.ID)
		s.scope.V(2).Info("Created new bastion host", "instance", instance.SelfLink)

	} else if err != nil {
		return err
	}

	if instance != nil {
		s.scope.GCPCluster.Status.Bastion.SelfLink = &instance.SelfLink
		instanceStatus := infrav1.InstanceStatus(instance.Status)
		s.scope.GCPCluster.Status.Bastion.InstanceStatus = &instanceStatus
	}

	s.scope.V(2).Info("Reconcile bastion completed successfully")
	return nil
}

// DeleteBastion deletes the Bastion instance
func (s *Service) DeleteBastion() error {
	network, err := s.networks.Get(s.scope.Project(), s.scope.NetworkName()).Do()
	if gcperrors.IsNotFound(err) {
		return nil
	}
	// Return early if the description doesn't match our ownership tag.
	if network.Description != infrav1.ClusterTagKey(s.scope.Name()) {
		return nil
	}

	instance, err := s.describeBastionInstance()
	if err != nil {
		if gcperrors.IsNotFound(err) {
			s.scope.V(2).Info("bastion instance does not exist")
			return nil
		}
		return errors.Wrap(err, "unable to describe bastion instance")
	}

	zone := s.getDefaultBastionZone()
	op, err := s.instances.Delete(s.scope.Project(), zone, instance.Name).Do()
	if err != nil {
		return errors.Wrap(err, "failed to terminate gcp instance")
	}

	if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
		return errors.Wrap(err, "failed to terminate gcp instance")
	}
	//record.Eventf(s.scope.GCPCluster, "SuccessfulTerminateBastion", "Terminated bastion instance %q", instance.Name)

	return nil
}

func (s *Service) describeBastionInstance() (*compute.Instance, error) {
	name := s.getDefaultBastionName()
	zone := s.getDefaultBastionZone()
	res, err := s.instances.Get(s.scope.Project(), zone, name).Do()

	if gcperrors.IsNotFound(err) {
		return nil, err
	} else if err != nil {
		return nil, errors.Wrapf(err, "failed to describe bastion instance: %s in zone %s", name, zone)
	}

	return res, nil
}

func (s *Service) getDefaultBastion() *compute.Instance {
	name := s.getDefaultBastionName()
	zone := s.getDefaultBastionZone()
	sourceImage := s.getDefaultBastionImage()
	machineType := s.getDefaultBastionMachineType()

	input := &compute.Instance{
		Name:         name,
		Zone:         zone,
		MachineType:  fmt.Sprintf("zones/%s/machineTypes/%s", zone, machineType),
		CanIpForward: true,
		NetworkInterfaces: []*compute.NetworkInterface{
			{
				Network: s.scope.NetworkSelfLink(),
				AccessConfigs: []*compute.AccessConfig{
					{
						Type: "ONE_TO_ONE_NAT",
						Name: "External NAT",
					},
				},
			},
		},
		// firewall to allow 22 port open
		Tags: &compute.Tags{
			Items: []string{fmt.Sprintf("%s-bastion", s.scope.Cluster.Name)},
		},
		Disks: []*compute.AttachedDisk{
			{
				AutoDelete: true,
				Boot:       true,
				InitializeParams: &compute.AttachedDiskInitializeParams{
					DiskSizeGb:  10,
					DiskType:    fmt.Sprintf("zones/%s/diskTypes/%s", zone, "pd-standard"),
					SourceImage: sourceImage,
				},
			},
		},
		Metadata: &compute.Metadata{},
		ServiceAccounts: []*compute.ServiceAccount{
			{
				Email: "default",
				Scopes: []string{
					compute.CloudPlatformScope,
				},
			},
		},
	}

	return input
}

func (s *Service) getDefaultBastionName() string {
	return fmt.Sprintf("%s-bastion", s.scope.Name())
}
func (s *Service) getDefaultBastionZone() string {
	return fmt.Sprintf("%s-a", s.scope.Region())
}
func (s *Service) getDefaultBastionImage() string {
	return "projects/ubuntu-os-cloud/global/images/family/ubuntu-minimal-1804-lts"
}
func (s *Service) getDefaultBastionMachineType() string {
	return "f1-micro"
}
