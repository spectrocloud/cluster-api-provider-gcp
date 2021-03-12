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
	"net/url"
	"path"
	"strconv"

	"github.com/pkg/errors"
	"google.golang.org/api/compute/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/gcperrors"
	"sigs.k8s.io/cluster-api-provider-gcp/cloud/wait"
)

func (s *Service) ReconcileFirewalls() error {
	for _, firewallSpec := range s.getFirewallSpecs() {
		// Get or create the firewall rules.
		firewall, err := s.firewalls.Get(s.scope.Project(), firewallSpec.Name).Do()
		if gcperrors.IsNotFound(err) {
			op, err := s.firewalls.Insert(s.scope.Project(), firewallSpec).Do()
			if err != nil {
				return errors.Wrapf(err, "failed to create firewall rule")
			}
			if err := wait.ForComputeOperation(s.scope.Compute, s.scope.Project(), op); err != nil {
				return errors.Wrapf(err, "failed to create firewall rule")
			}
			firewall, err = s.firewalls.Get(s.scope.Project(), firewallSpec.Name).Do()
			if err != nil {
				return errors.Wrapf(err, "failed to describe firewall rule")
			}
		} else if err != nil {
			return errors.Wrapf(err, "failed to describe firewall rule")
		}

		// Store in the Cluster Status.
		if s.scope.Network().FirewallRules == nil {
			s.scope.Network().FirewallRules = make(map[string]string)
		}
		s.scope.Network().FirewallRules[firewall.Name] = firewall.SelfLink
	}

	return nil
}

func (s *Service) DeleteFirewalls() error {
	for name := range s.scope.Network().FirewallRules {
		op, err := s.firewalls.Delete(s.scope.Project(), name).Do()
		if opErr := s.checkOrWaitForDeleteOp(op, err); opErr != nil {
			return errors.Wrapf(opErr, "failed to delete firewalls")
		}
		delete(s.scope.Network().FirewallRules, name)
	}

	//delete any additional non-default firewall rules that were generated
	//capg reconcile only deletes the default allow-* firewall rules
	firewallRules, err := s.firewalls.List(s.scope.Project()).Do()
	if err != nil {
		return errors.Wrapf(err, "failed to list firewall rules for project %s", s.scope.Project())
	}

	clusterName := s.scope.Name()
	for _, firewall := range firewallRules.Items {
		networkName, err := getFirewallNetworkName(firewall)
		if err != nil {
			return errors.Wrapf(err, "failed to get network name for firewall %s", firewall.Name)
		}

		//only handle rules for the cluster nw
		if networkName == s.scope.NetworkName() {
			for _, tt := range firewall.TargetTags {
				//only delete rules with target-tags containing the cluster-name
				if tt == clusterName {
					op, err := s.firewalls.Delete(s.scope.Project(), firewall.Name).Do()
					if opErr := s.checkOrWaitForDeleteOp(op, err); opErr != nil {
						return errors.Wrapf(opErr, "failed to delete firewall %s", firewall.Name)
					}
				}
			}
		}
	}

	return nil
}

//fetches the name of the network for a given firewall
func getFirewallNetworkName(firewall *compute.Firewall) (string, error) {
	//eg., url: projects/myproject/global/networks/my-network
	fireNetUrl := firewall.Network
	if len(fireNetUrl) == 0 {
		return "", nil
	}

	url, err := url.Parse(fireNetUrl)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse network url '%s' for firewall %s", firewall.Network, firewall.Name)
	}

	return path.Base(url.Path), nil
}

func (s *Service) getFirewallSpecs() []*compute.Firewall {
	return []*compute.Firewall{
		{
			Name:    fmt.Sprintf("allow-%s-%s-healthchecks", s.scope.Name(), infrav1.APIServerRoleTagValue),
			Network: s.scope.NetworkSelfLink(),
			Allowed: []*compute.FirewallAllowed{
				{
					IPProtocol: "TCP",
					Ports: []string{
						strconv.FormatInt(s.scope.LoadBalancerBackendPort(), 10),
					},
				},
			},
			Direction: "INGRESS",
			SourceRanges: []string{
				// Allow Google's internal IP ranges to perform health checks against our registered API servers.
				// For more information, https://cloud.google.com/load-balancing/docs/health-checks#fw-rule.
				"35.191.0.0/16",
				"130.211.0.0/22",
			},
			TargetTags: []string{
				fmt.Sprintf("%s-control-plane", s.scope.Name()),
			},
		},
		{
			Name:    fmt.Sprintf("allow-%s-%s-cluster", s.scope.Name(), infrav1.APIServerRoleTagValue),
			Network: s.scope.NetworkSelfLink(),
			Allowed: []*compute.FirewallAllowed{
				{
					IPProtocol: "all",
				},
			},
			Direction: "INGRESS",
			SourceTags: []string{
				fmt.Sprintf("%s-control-plane", s.scope.Name()),
				fmt.Sprintf("%s-node", s.scope.Name()),
			},
			TargetTags: []string{
				fmt.Sprintf("%s-control-plane", s.scope.Name()),
				fmt.Sprintf("%s-node", s.scope.Name()),
			},
		},
	}
}
