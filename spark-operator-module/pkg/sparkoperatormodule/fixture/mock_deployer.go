package fixture

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
)

type DeployCall struct {
	Resources []unstructured.Unstructured
}

type MockDeployer struct {
	mu    sync.Mutex
	calls []DeployCall
}

func (m *MockDeployer) Deploy(_ context.Context, input deploy.DeployInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, DeployCall{Resources: input.Resources})
	return nil
}

func (m *MockDeployer) LastCall() *DeployCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	last := m.calls[len(m.calls)-1]
	return &last
}

func (m *MockDeployer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}
