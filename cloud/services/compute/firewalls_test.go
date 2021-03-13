package compute

import (
	"testing"

	. "github.com/onsi/gomega"
	"google.golang.org/api/compute/v1"
)

func TestNewCrypto(t *testing.T) {
	g := NewGomegaWithT(t)

	firewall := &compute.Firewall{
		Network: "projects/myproject/global/networks/my-network",
	}

	networkName, err := getFirewallNetworkName(firewall)
	g.Expect(err).To(BeNil())
	g.Expect(networkName).Should(Equal("my-network"))
}
