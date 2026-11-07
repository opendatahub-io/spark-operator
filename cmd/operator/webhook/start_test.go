/*
Copyright 2026 The Kubeflow authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"crypto/tls"
	"errors"
	"testing"

	operatortls "github.com/kubeflow/spark-operator/v2/pkg/tls"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestManagerWithTLSProfile_FetchFailureDoesNotCreateManager(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "denied", err: errors.New("forbidden")},
		{name: "malformed", err: errors.New("invalid TLS profile")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			managerCreated := false
			_, _, _, err := newManagerWithTLSProfile(
				func() (operatortls.FetchResult, error) {
					return operatortls.FetchResult{}, tt.err
				},
				func([]func(*tls.Config)) (ctrl.Manager, error) {
					managerCreated = true
					return nil, nil
				},
			)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			if managerCreated {
				t.Fatal("manager factory must not run when TLS profile resolution fails")
			}
		})
	}
}

func TestNewMetricsOptions_Defaults(t *testing.T) {
	opts := newMetricsOptions(":8080", false, "", nil)

	if opts.BindAddress != ":8080" {
		t.Errorf("expected BindAddress :8080, got %q", opts.BindAddress)
	}
	if opts.SecureServing {
		t.Error("expected SecureServing=false")
	}
	if opts.CertDir != "" {
		t.Errorf("expected empty CertDir, got %q", opts.CertDir)
	}
	if opts.TLSOpts != nil {
		t.Error("expected nil TLSOpts")
	}
}

func TestNewMetricsOptions_SecureWithCertDir(t *testing.T) {
	tlsOpt := func(c *tls.Config) {
		c.MinVersion = tls.VersionTLS12
	}
	opts := newMetricsOptions(":8080", true, "/etc/metrics-certs", []func(*tls.Config){tlsOpt})

	if !opts.SecureServing {
		t.Error("expected SecureServing=true")
	}
	if opts.CertDir != "/etc/metrics-certs" {
		t.Errorf("expected CertDir /etc/metrics-certs, got %q", opts.CertDir)
	}
	if len(opts.TLSOpts) != 1 {
		t.Fatalf("expected 1 TLSOpt, got %d", len(opts.TLSOpts))
	}

	cfg := &tls.Config{}
	opts.TLSOpts[0](cfg)
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLSOpt not preserved: expected MinVersion TLS1.2, got %d", cfg.MinVersion)
	}
}

func TestNewMetricsOptions_SecureWithoutCertDir(t *testing.T) {
	opts := newMetricsOptions(":8080", true, "", nil)

	if !opts.SecureServing {
		t.Error("expected SecureServing=true")
	}
	if opts.CertDir != "" {
		t.Errorf("expected empty CertDir for self-signed fallback, got %q", opts.CertDir)
	}
}

func TestNewMetricsOptions_PreservesTLSOpts(t *testing.T) {
	opt1 := func(c *tls.Config) { c.MinVersion = tls.VersionTLS12 }
	opt2 := func(c *tls.Config) { c.NextProtos = []string{"h2"} }
	opts := newMetricsOptions(":9090", true, "/certs", []func(*tls.Config){opt1, opt2})

	if len(opts.TLSOpts) != 2 {
		t.Fatalf("expected 2 TLSOpts, got %d", len(opts.TLSOpts))
	}

	cfg := &tls.Config{}
	for _, fn := range opts.TLSOpts {
		fn(cfg)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("expected MinVersion TLS1.2, got %d", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "h2" {
		t.Errorf("expected NextProtos [h2], got %v", cfg.NextProtos)
	}
}
