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

package tls

import (
	"context"
	cryptotls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func apiServerWithProfile(profile map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "config.openshift.io/v1",
		"kind":       "APIServer",
		"metadata":   map[string]interface{}{"name": "cluster"},
		"spec":       map[string]interface{}{"tlsSecurityProfile": profile},
	}}
}

func customProfile(minVersion string, ciphers []string) map[string]interface{} {
	return map[string]interface{}{
		"type": "Custom",
		"custom": map[string]interface{}{
			"minTLSVersion": minVersion,
			"ciphers":       toIface(ciphers),
		},
	}
}

func customProfileWithGroups(groups []string) map[string]interface{} {
	profile := customProfile("VersionTLS12", []string{"ECDHE-RSA-AES128-GCM-SHA256"})
	custom := profile["custom"].(map[string]interface{})
	custom["groups"] = toIface(groups)
	return profile
}

func toIface(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func applyOpts(opts []func(*cryptotls.Config)) *cryptotls.Config {
	cfg := &cryptotls.Config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func TestParseTLSProfile(t *testing.T) {
	tests := []struct {
		name         string
		profile      map[string]interface{}
		wantMin      uint16
		wantCiphers  []uint16
		wantCurveIDs []cryptotls.CurveID
		wantErr      string
	}{
		{
			name:        "absent profile resolves Intermediate",
			profile:     nil,
			wantMin:     cryptotls.VersionTLS12,
			wantCiphers: IntermediateCiphers,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
			},
		},
		{
			name:        "Intermediate resolves explicit suites",
			profile:     map[string]interface{}{"type": "Intermediate"},
			wantMin:     cryptotls.VersionTLS12,
			wantCiphers: IntermediateCiphers,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
			},
		},
		{
			name:        "omitted type resolves Intermediate",
			profile:     map[string]interface{}{},
			wantMin:     cryptotls.VersionTLS12,
			wantCiphers: IntermediateCiphers,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
			},
		},
		{
			name:        "empty type resolves Intermediate",
			profile:     map[string]interface{}{"type": ""},
			wantMin:     cryptotls.VersionTLS12,
			wantCiphers: IntermediateCiphers,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
			},
		},
		{
			name:    "non-string type errors",
			profile: map[string]interface{}{"type": int64(42)},
			wantErr: "TLS profile type must be a string",
		},
		{
			name:    "Modern is runtime-managed",
			profile: map[string]interface{}{"type": "Modern"},
			wantMin: cryptotls.VersionTLS13,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
			},
		},
		{
			name:    "Custom with nil block errors",
			profile: map[string]interface{}{"type": "Custom"},
			wantErr: "no custom block",
		},
		{
			name:    "unknown type errors",
			profile: map[string]interface{}{"type": "SuperSecure"},
			wantErr: "unrecognized TLS profile type",
		},
		{
			name:    "Custom with unknown minimum version errors",
			profile: customProfile("VersionTLS99", []string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			wantErr: "unknown minimum TLS version",
		},
		{
			name:    "Custom with empty minimum version errors",
			profile: customProfile("", []string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			wantErr: "no minimum version",
		},
		{
			name:    "Custom below TLS 1.3 with no ciphers errors",
			profile: customProfile("VersionTLS12", nil),
			wantErr: "no cipher suites",
		},
		{
			name:    "Custom with all-unsupported ciphers errors",
			profile: customProfile("VersionTLS12", []string{"DHE-RSA-AES128-GCM-SHA256"}),
			wantErr: "no cipher suites usable",
		},
		{
			name:    "Custom TLS 1.2 with only TLS 1.3 suites errors",
			profile: customProfile("VersionTLS12", []string{"TLS_AES_128_GCM_SHA256"}),
			wantErr: "no cipher suites usable",
		},
		{
			name: "Custom with valid OpenSSL ciphers converts",
			profile: customProfile("VersionTLS12", []string{
				"ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384",
			}),
			wantMin: cryptotls.VersionTLS12,
			wantCiphers: []uint16{
				cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				cryptotls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
		},
		{
			name: "Custom TLS 1.2 with mixed suites keeps TLS 1.2 subset",
			profile: customProfile("VersionTLS12", []string{
				"TLS_AES_128_GCM_SHA256", "ECDHE-RSA-AES128-GCM-SHA256",
			}),
			wantMin:     cryptotls.VersionTLS12,
			wantCiphers: []uint16{cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		},
		{
			name:    "Custom IANA names accepted",
			profile: customProfile("VersionTLS12", []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}),
			wantMin: cryptotls.VersionTLS12,
			wantCiphers: []uint16{
				cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		},
		{
			name: "Custom with groups converts",
			profile: customProfileWithGroups([]string{
				"X25519", "secp256r1",
			}),
			wantMin: cryptotls.VersionTLS12,
			wantCurveIDs: []cryptotls.CurveID{
				cryptotls.X25519, cryptotls.CurveP256,
			},
		},
		{
			name:    "Custom with all-unusable groups errors",
			profile: customProfileWithGroups([]string{"unknown-group"}),
			wantErr: "TLS profile has no usable groups",
		},
		{
			name: "Custom with mixed groups keeps recognized subset",
			profile: customProfileWithGroups([]string{
				"unknown-group", "secp256r1",
			}),
			wantMin:      cryptotls.VersionTLS12,
			wantCurveIDs: []cryptotls.CurveID{cryptotls.CurveP256},
		},
		{
			name:         "Custom with empty groups keeps Go defaults",
			profile:      customProfileWithGroups(nil),
			wantMin:      cryptotls.VersionTLS12,
			wantCurveIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiServer := apiServerWithProfile(tt.profile)
			if tt.profile == nil {
				apiServer.Object["spec"] = map[string]interface{}{}
			}

			gotMin, gotCiphers, gotCurveIDs, _, err := parseTLSProfile(apiServer)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseTLSProfile() expected error containing %q, got nil", tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Errorf("parseTLSProfile() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTLSProfile() unexpected error: %v", err)
			}
			if gotMin != tt.wantMin {
				t.Errorf("parseTLSProfile() minVersion = %d, want %d", gotMin, tt.wantMin)
			}
			if tt.wantCiphers != nil {
				if len(gotCiphers) != len(tt.wantCiphers) {
					t.Fatalf("parseTLSProfile() ciphers = %v, want %v", gotCiphers, tt.wantCiphers)
				}
				for i, c := range gotCiphers {
					if c != tt.wantCiphers[i] {
						t.Errorf("parseTLSProfile() ciphers[%d] = %d, want %d", i, c, tt.wantCiphers[i])
					}
				}
			}
			if tt.wantCurveIDs == nil {
				if gotCurveIDs != nil {
					t.Errorf("parseTLSProfile() curve IDs = %v, want nil", gotCurveIDs)
				}
			} else if !slices.Equal(gotCurveIDs, tt.wantCurveIDs) {
				t.Errorf("parseTLSProfile() curve IDs = %v, want %v", gotCurveIDs, tt.wantCurveIDs)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestParseTLSProfile_OldExplicitSuites pins the HARD RULE: Old resolves to
// its actual TLS 1.0 minimum and its explicit suites, never a TLS 1.2 or
// Go-default replacement.
func TestParseTLSProfile_OldExplicitSuites(t *testing.T) {
	apiServer := apiServerWithProfile(map[string]interface{}{"type": "Old"})

	minVersion, ciphers, _, _, err := parseTLSProfile(apiServer)
	if err != nil {
		t.Fatalf("parseTLSProfile(Old) unexpected error: %v", err)
	}
	if minVersion != cryptotls.VersionTLS10 {
		t.Errorf("parseTLSProfile(Old) minVersion = %d, want TLS 1.0", minVersion)
	}
	if len(ciphers) == 0 {
		t.Fatal("parseTLSProfile(Old) must resolve explicit suites, not unrestricted defaults")
	}
	hasGCM, hasCBC, has3DES := false, false, false
	for _, id := range ciphers {
		switch id {
		case cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, cryptotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256:
			hasGCM = true
		case cryptotls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, cryptotls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA:
			hasCBC = true
		case cryptotls.TLS_RSA_WITH_3DES_EDE_CBC_SHA:
			has3DES = true
		}
	}
	if !hasGCM || !hasCBC || !has3DES {
		t.Errorf("parseTLSProfile(Old) suites must include Old's AES-GCM, CBC and 3DES entries, got %v", ciphers)
	}
}

func TestParseTLSProfile_CustomRejectsWeakCipherSuites(t *testing.T) {
	tests := []struct {
		name    string
		ciphers []string
	}{
		{name: "IANA RC4", ciphers: []string{"TLS_RSA_WITH_RC4_128_SHA"}},
		{name: "OpenSSL RC4", ciphers: []string{"ECDHE-RSA-RC4-SHA"}},
		{name: "IANA 3DES", ciphers: []string{"TLS_RSA_WITH_3DES_EDE_CBC_SHA"}},
		{name: "OpenSSL 3DES", ciphers: []string{"DES-CBC3-SHA"}},
		{name: "IANA SHA-1 CBC", ciphers: []string{"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA"}},
		{name: "OpenSSL SHA-1 CBC", ciphers: []string{"ECDHE-RSA-AES128-SHA"}},
		{name: "lowercase OpenSSL SHA-1 CBC", ciphers: []string{"ecdhe-rsa-aes128-sha"}},
		{
			name: "weak suite mixed with an approved suite",
			ciphers: []string{
				"ECDHE-RSA-AES128-GCM-SHA256",
				"ECDHE-RSA-AES128-SHA",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apiServer := apiServerWithProfile(customProfile("VersionTLS12", tc.ciphers))
			if _, _, _, _, err := parseTLSProfile(apiServer); err == nil || !strings.Contains(err.Error(), "disallowed cipher suite") {
				t.Fatalf("parseTLSProfile() error = %v, want disallowed cipher suite error", err)
			}
		})
	}
}

func TestFetchTLSProfileWithClient(t *testing.T) {
	t.Run("NoMatch returns hardened default with APIAvailable=false", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return &meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{
						Group: "config.openshift.io", Version: "v1", Resource: "apiservers",
					}}
				},
			}).Build()
		result, err := FetchTLSProfileWithClient(context.Background(), c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Fetched || result.APIAvailable {
			t.Errorf("expected Fetched=false, APIAvailable=false for NoMatch, got %+v", result)
		}
		if cfg := applyOpts(result.TLSOpts); cfg.MinVersion != cryptotls.VersionTLS12 {
			t.Errorf("expected TLS 1.2 default, got %d", cfg.MinVersion)
		}
	})

	t.Run("NotFound returns Intermediate fallback with APIAvailable=true for watcher recovery", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return apierrors.NewNotFound(schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, "cluster")
				},
			}).Build()
		result, err := FetchTLSProfileWithClient(context.Background(), c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Fetched {
			t.Errorf("expected Fetched=false for NotFound, got %+v", result)
		}
		if !result.APIAvailable {
			t.Error("expected APIAvailable=true for NotFound so the watcher catches the object's creation")
		}
		cfg := applyOpts(result.TLSOpts)
		if cfg.MinVersion != cryptotls.VersionTLS12 {
			t.Errorf("MinVersion = %d, want TLS 1.2 Intermediate fallback", cfg.MinVersion)
		}
		if len(cfg.CipherSuites) == 0 {
			t.Error("expected Intermediate fallback cipher suites")
		}
	})

	t.Run("transient error returns Intermediate fallback with APIAvailable=true", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return apierrors.NewServiceUnavailable("apiserver restarting")
				},
			}).Build()
		result, err := FetchTLSProfileWithClient(context.Background(), c)
		if err != nil {
			t.Fatalf("transient errors must fall back, not fail: %v", err)
		}
		if result.Fetched {
			t.Error("expected Fetched=false for transient")
		}
		if !result.APIAvailable {
			t.Error("expected APIAvailable=true so the watcher self-heals")
		}
		cfg := applyOpts(result.TLSOpts)
		if cfg.MinVersion != cryptotls.VersionTLS12 {
			t.Errorf("expected TLS 1.2 fallback, got %d", cfg.MinVersion)
		}
		if len(cfg.CipherSuites) == 0 {
			t.Error("expected Intermediate fallback cipher suites")
		}
	})

	t.Run("Forbidden fails closed", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					gr := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
					return apierrors.NewForbidden(gr, "cluster", errors.New("denied"))
				},
			}).Build()
		_, err := FetchTLSProfileWithClient(context.Background(), c)
		if err == nil {
			t.Fatal("Forbidden reads must fail closed, not fall back to Intermediate")
		}
	})

	t.Run("Unauthorized fails closed", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return apierrors.NewUnauthorized("authentication required")
				},
			}).Build()
		_, err := FetchTLSProfileWithClient(context.Background(), c)
		if err == nil {
			t.Fatal("Unauthorized reads must fail closed")
		}
	})

	t.Run("certificate verification errors wrapped in network errors fail closed", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
		}{
			{
				name: "hostname mismatch",
				err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: x509.HostnameError{Host: "api.example.invalid", Certificate: &x509.Certificate{}},
				},
			},
			{
				name: "unknown authority",
				err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				c := fake.NewClientBuilder().
					WithInterceptorFuncs(interceptor.Funcs{
						Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
							return tt.err
						},
					}).Build()
				result, err := FetchTLSProfileWithClient(context.Background(), c)
				if err == nil {
					t.Fatal("certificate verification failures must fail closed")
				}
				if result.Fetched || result.APIAvailable {
					t.Errorf("expected no fallback result, got %+v", result)
				}
				if !errors.Is(err, tt.err) {
					t.Errorf("error = %v, want wrapped certificate verification error", err)
				}
			})
		}
	})

	t.Run("unexpected API error fails closed", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return apierrors.NewInternalError(errors.New("apiserver hiccup"))
				},
			}).Build()
		_, err := FetchTLSProfileWithClient(context.Background(), c)
		if err == nil {
			t.Fatal("unexpected API errors must fail closed")
		}
	})

	t.Run("invalid profile fails closed", func(t *testing.T) {
		apiServer := apiServerWithProfile(customProfile("VersionTLS12", []string{"DHE-RSA-AES128-GCM-SHA256"}))
		c := fake.NewClientBuilder().WithObjects(apiServer).Build()
		_, err := FetchTLSProfileWithClient(context.Background(), c)
		if err == nil {
			t.Fatal("invalid profiles must fail closed")
		}
	})

	t.Run("groups are returned and applied", func(t *testing.T) {
		apiServer := apiServerWithProfile(customProfileWithGroups([]string{"X25519", "secp256r1"}))
		c := fake.NewClientBuilder().WithObjects(apiServer).Build()
		result, err := FetchTLSProfileWithClient(context.Background(), c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []cryptotls.CurveID{cryptotls.X25519, cryptotls.CurveP256}
		if !slices.Equal(result.CurveIDs, want) {
			t.Errorf("FetchTLSProfileWithClient() curve IDs = %v, want %v", result.CurveIDs, want)
		}
		cfg := applyOpts(result.TLSOpts)
		if !slices.Equal(cfg.CurvePreferences, want) {
			t.Errorf("applied curve preferences = %v, want %v", cfg.CurvePreferences, want)
		}
	})

	t.Run("successful fetch returns Modern profile", func(t *testing.T) {
		apiServer := apiServerWithProfile(map[string]interface{}{"type": "Modern"})
		c := fake.NewClientBuilder().WithObjects(apiServer).Build()
		result, err := FetchTLSProfileWithClient(context.Background(), c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Fetched || !result.APIAvailable {
			t.Errorf("expected Fetched=true, APIAvailable=true, got %+v", result)
		}
		cfg := applyOpts(result.TLSOpts)
		if cfg.MinVersion != cryptotls.VersionTLS13 {
			t.Errorf("expected TLS 1.3 for Modern, got %d", cfg.MinVersion)
		}
		if cfg.CipherSuites != nil {
			t.Errorf("TLS 1.3 cipher selection must stay Go-managed, got %v", cfg.CipherSuites)
		}
		if result.RawSpec == nil {
			t.Fatal("expected non-nil RawSpec for change detection")
		}
	})
}

func TestNextProtos(t *testing.T) {
	if got := NextProtos(cryptotls.VersionTLS12, nil); len(got) != 2 || got[0] != "h2" {
		t.Errorf("NextProtos(TLS1.2, defaults) = %v, want h2+http/1.1", got)
	}
	if got := NextProtos(cryptotls.VersionTLS12, IntermediateCiphers); len(got) != 2 || got[0] != "h2" {
		t.Errorf("NextProtos(TLS1.2, Intermediate) = %v, want h2+http/1.1", got)
	}
	// AES-256-GCM-only lists cannot serve HTTP/2 in Go: ALPN must drop h2.
	noH2 := []uint16{
		cryptotls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		cryptotls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
	if got := NextProtos(cryptotls.VersionTLS12, noH2); len(got) != 1 || got[0] != "http/1.1" {
		t.Errorf("NextProtos(TLS1.2, no-AES128-GCM) = %v, want http/1.1 only", got)
	}
	// A lower minimum does not prevent capable clients from negotiating TLS 1.2+.
	if got := NextProtos(cryptotls.VersionTLS10, IntermediateCiphers); len(got) != 2 || got[0] != "h2" {
		t.Errorf("NextProtos(TLS1.0, ciphers) = %v, want h2+http/1.1", got)
	}
}

func TestIsTransient(t *testing.T) {
	resource := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "service unavailable", err: apierrors.NewServiceUnavailable("down"), want: true},
		{name: "timeout", err: apierrors.NewTimeoutError("timed out", 0), want: true},
		{name: "context deadline exceeded", err: context.DeadlineExceeded, want: true},
		{
			name: "URL-wrapped context deadline exceeded",
			err:  &url.Error{Op: "Get", URL: "https://api.example.invalid", Err: context.DeadlineExceeded},
			want: true,
		},
		{name: "server timeout", err: apierrors.NewServerTimeout(resource, "get", 0), want: true},
		{name: "too many requests", err: apierrors.NewTooManyRequests("throttled", 0), want: true},
		{name: "DNS error", err: &net.DNSError{Err: "no such host"}, want: true},
		{name: "dial connection refused", err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, want: true},
		{name: "read timeout", err: &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, want: true},
		{name: "proxy connect with nested dial error", err: &net.OpError{Op: "proxyconnect", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, want: true},
		{name: "proxy connect without nested network operation", err: &net.OpError{Op: "proxyconnect", Err: errors.New("proxy rejected connection")}, want: false},
		{name: "unexpected network operation", err: &net.OpError{Op: "listen", Net: "tcp", Err: syscall.ECONNREFUSED}, want: false},
		{name: "EOF", err: io.EOF, want: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: true},
		{
			name: "URL-wrapped certificate verification error",
			err: &url.Error{
				Op:  "Get",
				URL: "https://api.example.invalid",
				Err: &cryptotls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
			},
			want: false,
		},
		{
			name: "wrapped network error",
			err:  fmt.Errorf("request failed: %w", &net.DNSError{Err: "network failure"}),
			want: true,
		},
		{name: "not found", err: apierrors.NewNotFound(resource, "cluster"), want: false},
		{name: "forbidden", err: apierrors.NewForbidden(resource, "cluster", errors.New("denied")), want: false},
		{name: "unexpected API status", err: apierrors.NewInternalError(errors.New("server error")), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransient(tt.err); got != tt.want {
				t.Errorf("isTransient(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestParseTLSProfile_SecPMLKEMGroupsValidateOnGo126(t *testing.T) {
	// The SecP*MLKEM* groups are implemented by crypto/tls from Go 1.26;
	// the module requires that toolchain, so a Custom profile listing only
	// them must validate instead of leaving handshakes with no group.
	profile := customProfileWithGroups([]string{"SecP256r1MLKEM768", "SecP384r1MLKEM1024"})
	profile["custom"].(map[string]interface{})["minTLSVersion"] = "VersionTLS13"
	apiServer := apiServerWithProfile(profile)
	_, _, curveIDs, _, err := parseTLSProfile(apiServer)
	if err != nil {
		t.Fatalf("parseTLSProfile() error = %v", err)
	}
	want := []cryptotls.CurveID{cryptotls.SecP256r1MLKEM768, cryptotls.SecP384r1MLKEM1024}
	if len(curveIDs) != len(want) {
		t.Fatalf("curveIDs = %v, want %v", curveIDs, want)
	}
	for i, id := range want {
		if curveIDs[i] != id {
			t.Fatalf("curveIDs = %v, want %v", curveIDs, want)
		}
	}
}

func TestParseTLSProfile_HybridOnlyGroupsRequireTLS13(t *testing.T) {
	// X25519MLKEM768 and SecP*MLKEM* are TLS 1.3-only: a profile that lists
	// nothing else cannot complete a TLS <=1.2 handshake.
	hybrids := []string{"X25519MLKEM768", "SecP256r1MLKEM768"}

	for _, minVersion := range []string{"VersionTLS10", "VersionTLS11", "VersionTLS12"} {
		t.Run(minVersion, func(t *testing.T) {
			profile := customProfileWithGroups(hybrids)
			profile["custom"].(map[string]interface{})["minTLSVersion"] = minVersion

			_, _, _, _, err := parseTLSProfile(apiServerWithProfile(profile))
			if err == nil || !strings.Contains(err.Error(), "no groups usable at that version") {
				t.Fatalf("parseTLSProfile(%s, hybrid-only groups) error = %v, want unusable groups error", minVersion, err)
			}
		})
	}

	t.Run("mixed list keeps the TLS 1.2 capable group", func(t *testing.T) {
		profile := customProfileWithGroups([]string{"X25519MLKEM768", "X25519"})

		_, _, curveIDs, _, err := parseTLSProfile(apiServerWithProfile(profile))
		if err != nil {
			t.Fatalf("parseTLSProfile() unexpected error: %v", err)
		}
		want := []cryptotls.CurveID{cryptotls.X25519MLKEM768, cryptotls.X25519}
		if !slices.Equal(curveIDs, want) {
			t.Errorf("curveIDs = %v, want %v", curveIDs, want)
		}
	})
}

// Released operators honor the cluster profile with no tlsAdherence set (the
// field is FeatureGate-gated and unset on every supported OCP 4.x cluster),
// so an upgrade must preserve that: a Modern profile keeps serving TLS 1.3.
func TestFetchTLSProfileWithClient_ModernWithUnsetAdherence(t *testing.T) {
	apiServer := apiServerWithProfile(map[string]interface{}{"type": "Modern"})
	c := fake.NewClientBuilder().WithObjects(apiServer).Build()

	result, err := FetchTLSProfileWithClient(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := applyOpts(result.TLSOpts)
	if cfg.MinVersion != cryptotls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3 honored under unset adherence", cfg.MinVersion)
	}
	// Hybrid-first groups preserved: Modern pins X25519MLKEM768 ahead of the
	// classic curves, matching the pre-PR outcome.
	want := []cryptotls.CurveID{
		cryptotls.X25519MLKEM768, cryptotls.X25519, cryptotls.CurveP256, cryptotls.CurveP384,
	}
	if !slices.Equal(cfg.CurvePreferences, want) {
		t.Errorf("CurvePreferences = %v, want %v", cfg.CurvePreferences, want)
	}
}
