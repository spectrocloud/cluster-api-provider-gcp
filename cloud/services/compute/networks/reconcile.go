/*
Copyright 2021 The Kubernetes Authors.

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

package networks

import (
	"context"
	"strings"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/filter"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"google.golang.org/api/compute/v1"

	"k8s.io/utils/ptr"

	infrav1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/gcperrors"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	k8sNodeRouteTag = "k8s-node-route"
)

// Reconcile reconcile cluster network components.
func (s *Service) Reconcile(ctx context.Context) error {
	log := log.FromContext(ctx)
	log.Info("Reconciling network resources")
	network, err := s.createOrGetNetwork(ctx)
	if err != nil {
		return err
	}
	s.scope.Network().SelfLink = ptr.To[string](network.SelfLink)

	if !network.AutoCreateSubnetworks {
		// Custom mode detected
		for _, subnet := range s.scope.SubnetworkSpec() {
			if _, err = s.createOrGetSubNetwork(ctx, subnet); err != nil {
				return err
			}
		}
	}

	if network.Description == infrav1.ClusterTagKey(s.scope.Name()) {
		router, err := s.createOrGetRouter(ctx, network)
		if err != nil {
			return err
		}

		s.scope.Network().Router = ptr.To[string](router.SelfLink)
	}

	s.scope.Network().SelfLink = ptr.To[string](network.SelfLink)
	return nil
}

// Delete delete cluster network components.
func (s *Service) Delete(ctx context.Context) error {
	log := log.FromContext(ctx)
	if s.scope.IsSharedVpc() {
		log.V(2).Info("Shared VPC enabled. Ignore Deleting network resources")
		s.scope.Network().Router = nil
		s.scope.Network().SelfLink = nil
		return nil
	}
	log.Info("Deleting network resources")
	networkKey := meta.GlobalKey(s.scope.NetworkName())
	log.V(2).Info("Looking for network before deleting", "name", networkKey)
	network, err := s.networks.Get(ctx, networkKey)
	if err != nil {
		return gcperrors.IgnoreNotFound(err)
	}

	if network.Description != infrav1.ClusterTagKey(s.scope.Name()) {
		return nil
	}

	log.V(2).Info("Found network created by capg", "name", s.scope.NetworkName())

	routerSpec := s.scope.NatRouterSpec()
	routerKey := meta.RegionalKey(routerSpec.Name, s.scope.Region())
	log.V(2).Info("Looking for cloudnat router before deleting", "name", routerSpec.Name)
	router, err := s.routers.Get(ctx, routerKey)
	if err != nil && !gcperrors.IsNotFound(err) {
		return err
	}

	if router != nil && router.Description == infrav1.ClusterTagKey(s.scope.Name()) {
		if err := s.routers.Delete(ctx, routerKey); err != nil && !gcperrors.IsNotFound(err) {
			return err
		}
	}

	if !network.AutoCreateSubnetworks {
		// Custom mode detected
		for _, subnet := range s.scope.SubnetworkSpec() {
			subnetKey := meta.RegionalKey(subnet.Name, subnet.Region)
			subnetwork, err := s.subnetworks.Get(ctx, subnetKey)
			if err != nil && !gcperrors.IsNotFound(err) {
				return err
			}
			if subnetwork != nil {
				if err = s.subnetworks.Delete(ctx, subnetKey); err != nil {
					log.Error(err, "Error deleting a subnetwork", "name", subnet.Name, "region", subnet.Region)
					return err
				}
			}
		}
	}

	// Delete routes associated with network
	fl := filter.Regexp("description", k8sNodeRouteTag)
	routeList, err := s.routes.List(ctx, fl)
	if err != nil {
		log.Error(err, "failed to list routes for the cluster")
		return err
	}

	for _, route := range routeList {
		if strings.HasSuffix(route.Network, s.scope.NetworkName()) {
			log.V(2).Info("Deleting route ", "route:", route.Name)
			err := s.routes.Delete(ctx, meta.GlobalKey(route.Name))
			if err != nil {
				log.Error(err, "Error deleting a route", "name", route.Name)
				return err
			}
		}
	}

	if err := s.networks.Delete(ctx, networkKey); err != nil {
		log.Error(err, "Error deleting a network", "name", s.scope.NetworkName())
		return err
	}

	s.scope.Network().Router = nil
	s.scope.Network().SelfLink = nil
	return nil
}

// createOrGetNetwork creates a network if not exist otherwise return existing network.
func (s *Service) createOrGetNetwork(ctx context.Context) (*compute.Network, error) {
	log := log.FromContext(ctx)
	log.V(2).Info("Looking for network", "name", s.scope.NetworkName())
	networkKey := meta.GlobalKey(s.scope.NetworkName())
	network, err := s.networks.Get(ctx, networkKey)
	if err != nil {
		if !gcperrors.IsNotFound(err) {
			log.Error(err, "Error looking for network", "name", s.scope.NetworkName())
			return nil, err
		}

		if s.scope.IsSharedVpc() {
			log.Error(err, "Shared VPC is enabled, but could not find existing network", "name", s.scope.NetworkName())
			return nil, err
		}

		log.V(2).Info("Creating a network", "name", s.scope.NetworkName())
		if err := s.networks.Insert(ctx, networkKey, s.scope.NetworkSpec()); err != nil {
			log.Error(err, "Error creating a network", "name", s.scope.NetworkName())
			return nil, err
		}

		network, err = s.networks.Get(ctx, networkKey)
		if err != nil {
			return nil, err
		}
	}

	return network, nil
}

// createOrGetSubNetwork creates a subnetwork if not exist otherwise return existing subnetwork.
func (s *Service) createOrGetSubNetwork(ctx context.Context, subnet *compute.Subnetwork) (*compute.Subnetwork, error) {
	log := log.FromContext(ctx)
	log.V(2).Info("Looking for subnetwork", "name", subnet.Name)
	subnetKey := meta.RegionalKey(subnet.Name, subnet.Region)
	subnetwork, err := s.subnetworks.Get(ctx, subnetKey)
	if err != nil {
		if !gcperrors.IsNotFound(err) {
			log.Error(err, "Error looking for subnetwork", "name", subnet.Name)
			return nil, err
		}

		log.V(2).Info("Creating a subnetwork", "name", subnet.Name)
		if err := s.subnetworks.Insert(ctx, subnetKey, subnet); err != nil {
			log.Error(err, "Error creating a subnetwork", "name", subnet.Name)
			return nil, err
		}

		subnetwork, err = s.subnetworks.Get(ctx, subnetKey)
		if err != nil {
			return nil, err
		}
	}

	return subnetwork, nil
}

// createOrGetRouter creates a cloudnat router if not exist otherwise return the existing.
func (s *Service) createOrGetRouter(ctx context.Context, network *compute.Network) (*compute.Router, error) {
	log := log.FromContext(ctx)
	spec := s.scope.NatRouterSpec()
	log.V(2).Info("Looking for cloudnat router", "name", spec.Name)
	routerKey := meta.RegionalKey(spec.Name, s.scope.Region())
	router, err := s.routers.Get(ctx, routerKey)
	if err != nil {
		if !gcperrors.IsNotFound(err) {
			log.Error(err, "Error looking for cloudnat router", "name", spec.Name)
			return nil, err
		}

		if s.scope.IsSharedVpc() {
			log.Error(err, "Shared VPC is enabled, but could not find existing router", "name", routerKey)
			return nil, err
		}

		spec.Network = network.SelfLink
		spec.Description = infrav1.ClusterTagKey(s.scope.Name())
		log.V(2).Info("Creating a cloudnat router", "name", spec.Name)
		if err := s.routers.Insert(ctx, routerKey, spec); err != nil {
			log.Error(err, "Error creating a cloudnat router", "name", spec.Name)
			return nil, err
		}

		router, err = s.routers.Get(ctx, routerKey)
		if err != nil {
			return nil, err
		}
	}

	return router, nil
}
