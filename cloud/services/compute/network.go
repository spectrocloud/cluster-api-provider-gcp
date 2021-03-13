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
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/api/compute/v1"
	"k8s.io/utils/pointer"

	infrav1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/gcperrors"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/wait"
)

const (
	k8sNodeRouteTag = "k8s-node-route"
)

// InstanceIfExists returns the existing instance or nothing if it doesn't exist.
func (s *Service) ReconcileNetwork() error {
	// Create Network
	spec := s.getNetworkSpec()
	network, err := s.networks.Get(s.scope.Project(), spec.Name).Do()
	autoCreateCloudNat := false
	if gcperrors.IsNotFound(err) {
		autoCreateCloudNat = true
		op, err := s.networks.Insert(s.scope.Project(), spec).Do()
		if err != nil {
			return errors.Wrapf(err, "failed to create network")
		}
		if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
			return errors.Wrapf(err, "failed to create network")
		}
		network, err = s.networks.Get(s.scope.Project(), spec.Name).Do()
		if err != nil {
			return errors.Wrapf(err, "failed to describe network")
		}
		s.scope.Info("Created VPC network", "network", spec.Name)
	} else if err != nil {
		return errors.Wrapf(err, "failed to describe network")
	}

	if autoCreateCloudNat {
		if err := s.createCloudNat(network); err != nil {
			return errors.Wrapf(err, "failed to create cloudnat gateway")
		}
	}

	s.scope.GCPCluster.Spec.Network.Name = pointer.StringPtr(network.Name)
	s.scope.GCPCluster.Spec.Network.AutoCreateSubnetworks = pointer.BoolPtr(network.AutoCreateSubnetworks)
	s.scope.GCPCluster.Status.Network.SelfLink = pointer.StringPtr(network.SelfLink)

	// Create subnetworks if the network is created in custom mode
	if !network.AutoCreateSubnetworks {
		if err := s.createSubnetworks(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) getNetworkSpec() *compute.Network {
	res := &compute.Network{
		Name:                  s.scope.NetworkName(),
		Description:           infrav1.ClusterTagKey(s.scope.Name()),
		AutoCreateSubnetworks: true,
	}

	if s.scope.GCPCluster.Spec.Network.AutoCreateSubnetworks != nil {
		res.AutoCreateSubnetworks = *s.scope.GCPCluster.Spec.Network.AutoCreateSubnetworks
		if !res.AutoCreateSubnetworks {
			// res.AutoCreateSubnetworks holds a boolean value. If set to true, VPC network is created in
			// auto mode. If false, then network gets created in legacy mode(unset)
			// Hence add 'AutoCreateSubnetworks' even though the value is set to false so as to
			// distinguish between unset(legacy) and explicitly set false(custom)
			res.ForceSendFields = []string{"AutoCreateSubnetworks"}
		}
	}

	return res
}

func (s *Service) DeleteNetwork() error {
	network, err := s.networks.Get(s.scope.Project(), s.scope.NetworkName()).Do()
	if gcperrors.IsNotFound(err) {
		return nil
	}

	// Return early if the description doesn't match our ownership tag.
	if network.Description != infrav1.ClusterTagKey(s.scope.Name()) {
		return nil
	}

	// Delete Router.
	router, err := s.routers.Get(s.scope.Project(), s.scope.Region(), getRouterName(s.scope.NetworkName())).Do()
	if err == nil {
		op, err := s.routers.Delete(s.scope.Project(), s.scope.Region(), router.Name).Do()
		if opErr := s.checkOrWaitForDeleteOp(op, err); opErr != nil {
			return errors.Wrapf(opErr, "failed to delete router")
		}
	} else {
		if !gcperrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get router to delete")
		}
	}

	// Delete routes associated with network
	filterString := fmt.Sprintf("description=%s name=%s-*", k8sNodeRouteTag, s.scope.Name())
	routeList, err := s.routes.List(s.scope.Project()).Filter(filterString).Do()
	if err != nil {
		return errors.Wrapf(err, "failed to list routes for the cluster")
	}

	for _, route := range routeList.Items {
		if strings.HasSuffix(route.Network, network.Name) {
			s.scope.Info("deleting route ", "route:", route.Name)
			op, err := s.routes.Delete(s.scope.Project(), route.Name).Do()
			if opErr := s.checkOrWaitForDeleteOp(op, err); opErr != nil {
				return errors.Wrapf(opErr, "failed to delete route")
			}
		}
	}

	// Delete subnetworks if the network was created in custom mode
	if !network.AutoCreateSubnetworks {
		if err := s.deleteSubnetworks(network.Subnetworks); err != nil {
			return err
		}
	}

	// Delete Network.
	op, err := s.networks.Delete(s.scope.Project(), network.Name).Do()
	if opErr := s.checkOrWaitForDeleteOp(op, err); opErr != nil {
		return errors.Wrapf(opErr, "failed to delete network")
	}
	s.scope.GCPCluster.Spec.Network.Name = nil
	return nil
}

func (s *Service) createCloudNat(network *compute.Network) error {
	router, err := s.routers.Get(s.scope.Project(), s.scope.Region(), getRouterName(s.scope.NetworkName())).Do()
	if gcperrors.IsNotFound(err) {
		router = s.getRouterSpec(network)
		op, err := s.routers.Insert(s.scope.Project(), s.scope.Region(), router).Do()
		if err != nil {
			return errors.Wrapf(err, "failed to create router")
		}
		if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
			return errors.Wrapf(err, "failed to wait for create router operation")
		}
		router, err = s.routers.Get(s.scope.Project(), s.scope.Region(), router.Name).Do()
		if err != nil {
			return errors.Wrapf(err, "failed to get router after create")
		}
	} else if err != nil {
		return errors.Wrapf(err, "failed to get routers")
	}

	if len(router.Nats) == 0 {
		router.Nats = []*compute.RouterNat{s.getRouterNatSpec()}
		op, err := s.routers.Patch(s.scope.Project(), s.scope.Region(), router.Name, router).Do()
		if err != nil {
			return errors.Wrapf(err, "failed to patch router to create nat")
		}
		if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
			return errors.Wrapf(err, "failed to wait for patch router operation")
		}
	}

	s.scope.GCPCluster.Status.Network.Router = pointer.StringPtr(router.SelfLink)
	return nil
}

func (s *Service) getRouterSpec(network *compute.Network) *compute.Router {
	return &compute.Router{
		Name:    getRouterName(network.Name),
		Network: network.SelfLink,
		Nats:    []*compute.RouterNat{s.getRouterNatSpec()},
	}
}

func (s *Service) getRouterNatSpec() *compute.RouterNat {
	return &compute.RouterNat{
		Name:                          getRouterNatName(s.scope.NetworkName()),
		NatIpAllocateOption:           "AUTO_ONLY",
		SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES",
	}
}

func getRouterName(network string) string {
	return fmt.Sprintf("%s-%s", network, "router")
}
func getRouterNatName(network string) string {
	return fmt.Sprintf("%s-%s", network, "nat")
}

func (s *Service) getSubnetworkSpec(subnet *infrav1.SubnetSpec) *compute.Subnetwork {
	res := &compute.Subnetwork{
		EnableFlowLogs: *subnet.EnableFlowLogs,
		IpCidrRange:    subnet.CidrBlock,
		Name:           subnet.Name,
		Network:        s.scope.NetworkSelfLink(),
		Region:         subnet.Region,
	}
	return res
}

func (s *Service) createSubnetworks() error {
	for _, subnet := range s.scope.Subnets() {
		_, err := s.subnetworks.Get(s.scope.Project(), s.scope.Region(), subnet.Name).Do()
		if gcperrors.IsNotFound(err) {
			subnetSpec := s.getSubnetworkSpec(subnet)
			op, err := s.subnetworks.Insert(s.scope.Project(), s.scope.Region(), subnetSpec).Do()
			if err != nil {
				return errors.Wrapf(err, "failed to create subnetwork")
			}
			if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
				return errors.Wrapf(err, "failed to wait create subnetwork")
			}
			_, err = s.subnetworks.Get(s.scope.Project(), s.scope.Region(), subnetSpec.Name).Do()
			if err != nil {
				return errors.Wrapf(err, "failed to describe subnetwork")
			}
			s.scope.Info("Created subnetwork", "subnet", subnetSpec.Name, "region", subnetSpec.Region)
		} else if err != nil {
			return errors.Wrapf(err, "failed to describe subnetwork")
		}
	}
	return nil
}

func (s *Service) deleteSubnetworks(subnetworks []string) error {
	for _, subnet := range subnetworks {
		subnetName := s.getSubnetNameFromUrl(subnet)
		if subnetName != "" {
			op, err := s.subnetworks.Delete(s.scope.Project(), s.scope.Region(), subnetName).Do()
			if err != nil {
				return errors.Wrapf(err, "failed to delete subnetwork %s", subnet)
			}
			if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
				return errors.Wrapf(err, "failed to wait delete subnetwork %s", subnet)
			}
		}
	}
	return nil
}

// Subnetworks list holds the fully-qualified URLs for all subnetworks in the VPC network
// URL is like: https://www.googleapis.com/compute/v1/projects/project1/regions/us-central1/subnetworks/subnetwork1
// This function gets the subnetwork name from the URL
func (s *Service) getSubnetNameFromUrl(subnetUrl string) string {
	subnetName := ""
	splitSlice := strings.Split(subnetUrl, "/")
	if len(splitSlice) > 0 {
		subnetName = splitSlice[len(splitSlice)-1]
	}
	return subnetName
}
