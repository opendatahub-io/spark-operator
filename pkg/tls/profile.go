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
	stderrors "errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	ocpcrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var profileLog = ctrl.Log.WithName("tls-profile")

// FetchResult carries the resolved TLS configuration for the operator's
// webhook and metrics servers.
type FetchResult struct {
	TLSOpts []func(*cryptotls.Config)
	// Fetched reports whether the cluster TLS profile was read successfully.
	Fetched bool
	// APIAvailable reports whether the config.openshift.io API is served, so
	// the profile watcher can register and self-heal after fallbacks.
	APIAvailable bool
	// RawSpec is the raw tlsSecurityProfile for change detection.
	RawSpec map[string]interface{}
	// MinVersion and CipherSuites are exposed so callers can derive ALPN from
	// the effective configuration. CurveIDs are the resolved group preferences.
	MinVersion   uint16
	CipherSuites []uint16
	CurveIDs     []cryptotls.CurveID
}

var APIServerGVK = schema.GroupVersionKind{
	Group: "config.openshift.io", Version: "v1", Kind: "APIServer",
}

// FetchTLSProfile reads the cluster TLS profile from
// apiservers.config.openshift.io/cluster.
//
// Fallback and error semantics:
//   - config.openshift.io API not served (non-OpenShift cluster) or the
//     APIServer object missing (bootstrap race): hardened defaults, no error.
//   - transient API error: Intermediate fallback with APIAvailable set so the
//     watcher self-heals once the read recovers.
//   - Forbidden or Unauthorized reads, any other API error, or a profile that
//     is malformed or unusable by this Go runtime: error. Callers must fail
//     startup rather than serve a guessed posture.
func FetchTLSProfile(cfg *rest.Config, scheme *runtime.Scheme) (FetchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return FetchResult{}, fmt.Errorf("creating bootstrap client for TLS profile: %w", err)
	}

	return FetchTLSProfileWithClient(ctx, c)
}

func FetchTLSProfileWithClient(ctx context.Context, c client.Client) (FetchResult, error) {
	apiServer := &unstructured.Unstructured{}
	apiServer.SetGroupVersionKind(APIServerGVK)

	if err := c.Get(ctx, client.ObjectKey{Name: "cluster"}, apiServer); err != nil {
		if meta.IsNoMatchError(err) {
			profileLog.Info("TLS profile not available (non-OpenShift cluster)")
			return hardenedDefault(), nil
		}
		if errors.IsNotFound(err) {
			// The API is served but the APIServer object is missing (bootstrap
			// race on a fresh OpenShift install): keep the result watchable so
			// the profile watcher catches the object's creation instead of
			// serving the fallback until an unrelated process restart.
			profileLog.Info("APIServer resource not found, using Intermediate fallback with watcher")
			result := intermediateFallback()
			return result, nil
		}
		if isTransient(err) {
			profileLog.Info("Transient error reading APIServer TLS profile, using Intermediate fallback", "error", err)
			return intermediateFallback(), nil
		}
		return FetchResult{}, fmt.Errorf("reading APIServer TLS profile: %w", err)
	}

	minVersion, ciphers, curveIDs, rawSpec, err := parseTLSProfile(apiServer)
	if err != nil {
		return FetchResult{}, fmt.Errorf("invalid cluster TLS profile: %w", err)
	}
	profileLog.Info("Applying cluster TLS profile", "minVersion", minVersion, "ciphers", len(ciphers),
		"groups", len(curveIDs))

	return FetchResult{
		TLSOpts: []func(*cryptotls.Config){
			func(c *cryptotls.Config) {
				c.MinVersion = minVersion
				if len(ciphers) > 0 {
					c.CipherSuites = ciphers
				}
				if curveIDs != nil {
					c.CurvePreferences = curveIDs
				}
			},
		},
		Fetched:      true,
		APIAvailable: true,
		RawSpec:      rawSpec,
		MinVersion:   minVersion,
		CipherSuites: ciphers,
		CurveIDs:     curveIDs,
	}, nil
}

func isTransient(err error) bool {
	if errors.IsServiceUnavailable(err) ||
		errors.IsTimeout(err) ||
		errors.IsServerTimeout(err) ||
		errors.IsTooManyRequests(err) {
		return true
	}
	var statusErr *errors.StatusError
	if stderrors.As(err, &statusErr) {
		return false
	}
	var verificationErr *cryptotls.CertificateVerificationError
	if stderrors.As(err, &verificationErr) {
		return false
	}
	var hostnameErr x509.HostnameError
	if stderrors.As(err, &hostnameErr) {
		return false
	}
	var authorityErr x509.UnknownAuthorityError
	if stderrors.As(err, &authorityErr) {
		return false
	}
	var timeoutErr interface{ Timeout() bool }
	if stderrors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if stderrors.As(err, &dnsErr) || stderrors.Is(err, io.EOF) || stderrors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if !stderrors.As(err, &opErr) {
		return false
	}
	switch opErr.Op {
	case "dial", "read", "write":
		return true
	case "proxyconnect":
		var nestedOpErr *net.OpError
		if stderrors.As(opErr.Err, &nestedOpErr) {
			switch nestedOpErr.Op {
			case "dial", "read", "write":
				return true
			}
		}
	}
	return false
}

func hardenedDefault() FetchResult {
	return FetchResult{
		TLSOpts: []func(*cryptotls.Config){
			func(c *cryptotls.Config) { c.MinVersion = cryptotls.VersionTLS12 },
		},
		MinVersion: cryptotls.VersionTLS12,
	}
}

func intermediateFallback() FetchResult {
	return FetchResult{
		TLSOpts: []func(*cryptotls.Config){
			func(c *cryptotls.Config) {
				c.MinVersion = cryptotls.VersionTLS12
				c.CipherSuites = IntermediateCiphers
			},
		},
		Fetched:      false,
		APIAvailable: true,
		MinVersion:   cryptotls.VersionTLS12,
		CipherSuites: IntermediateCiphers,
	}
}

// IntermediateCiphers is the Mozilla Intermediate cipher suite set.
var IntermediateCiphers = []uint16{
	cryptotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	cryptotls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	cryptotls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	cryptotls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	cryptotls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// parseTLSProfile converts the APIServer's tlsSecurityProfile into an
// explicit minimum version, cipher-suite list, and curve preferences. Built-in profiles resolve
// from the OpenShift profile definitions; malformed input (unknown type, a
// Custom block that is missing, an unknown or missing minimum version, or a
// cipher list with no suite usable by this Go runtime) returns an error
// instead of a silent substitution. Old resolves to its actual minimum and
// explicit suites, never a TLS 1.2/Go-default replacement.
func parseTLSProfile(
	apiServer *unstructured.Unstructured,
) (uint16, []uint16, []cryptotls.CurveID, map[string]interface{}, error) {
	profile, found, err := unstructured.NestedMap(apiServer.Object, "spec", "tlsSecurityProfile")
	if err != nil {
		return 0, nil, nil, nil, fmt.Errorf("reading tlsSecurityProfile: %w", err)
	}
	if !found || profile == nil {
		return parseSuites(configv1.TLSProfiles[configv1.TLSProfileIntermediateType], nil)
	}

	profileType := ""
	if rawType, found := profile["type"]; found {
		var ok bool
		profileType, ok = rawType.(string)
		if !ok {
			return 0, nil, nil, nil, fmt.Errorf("TLS profile type must be a string, got %T", rawType)
		}
	}
	switch configv1.TLSProfileType(profileType) {
	case configv1.TLSProfileIntermediateType, "":
		return parseSuites(configv1.TLSProfiles[configv1.TLSProfileIntermediateType], profile)
	case configv1.TLSProfileModernType:
		return parseSuites(configv1.TLSProfiles[configv1.TLSProfileModernType], profile)
	// tls-lint:ignore (tls-profile-old) — honoring Old exactly (TLS 1.0
	// minimum and its explicit suites) is required by the OpenShift
	// centralized TLS configuration contract; silently substituting a
	// stronger profile is the defect this code fixes.
	case configv1.TLSProfileOldType: // tls-lint:ignore
		return parseSuites(configv1.TLSProfiles[configv1.TLSProfileOldType], profile) // tls-lint:ignore
	// tls-lint:ignore (tls-profile-custom) — the Custom profile is resolved
	// from administrator input and validated against the running Go runtime
	// below; this switch handles every profile type by contract.
	case configv1.TLSProfileCustomType: // tls-lint:ignore
		custom, _, err := unstructured.NestedMap(profile, "custom")
		if err != nil {
			return 0, nil, nil, nil, fmt.Errorf("reading custom TLS profile: %w", err)
		}
		if custom == nil {
			return 0, nil, nil, nil, fmt.Errorf("custom TLS profile type set but no custom block provided")
		}
		minVer, _ := custom["minTLSVersion"].(string)
		cipherNames, _, err := unstructured.NestedStringSlice(custom, "ciphers")
		if err != nil {
			return 0, nil, nil, nil, fmt.Errorf("reading ciphers from custom TLS profile: %w", err)
		}
		if err := validateCustomCipherSuites(cipherNames); err != nil {
			return 0, nil, nil, nil, err
		}
		groupNames, _, err := unstructured.NestedStringSlice(custom, "groups")
		if err != nil {
			return 0, nil, nil, nil, fmt.Errorf("reading groups from custom TLS profile: %w", err)
		}
		groups := make([]configv1.TLSGroup, len(groupNames))
		for i, group := range groupNames {
			groups[i] = configv1.TLSGroup(group)
		}
		spec := &configv1.TLSProfileSpec{
			MinTLSVersion: configv1.TLSProtocolVersion(minVer),
			Ciphers:       cipherNames,
			Groups:        groups,
		}
		return parseSuites(spec, profile)
	default:
		return 0, nil, nil, nil, fmt.Errorf("unrecognized TLS profile type %q", profileType)
	}
}

// tlsGroupToCurveID maps OpenShift TLS group names to Go CurveID values.
// Local implementation because the pinned library-go version does not export
// TLSGroupsToCurveIDs (requires k8s 0.36).
var tlsGroupToCurveID = map[configv1.TLSGroup]cryptotls.CurveID{
	configv1.TLSGroupSecP256r1:          cryptotls.CurveP256,
	configv1.TLSGroupSecP384r1:          cryptotls.CurveP384,
	configv1.TLSGroupSecP521r1:          cryptotls.CurveP521,
	configv1.TLSGroupX25519:             cryptotls.X25519,
	configv1.TLSGroupX25519MLKEM768:     cryptotls.X25519MLKEM768,
	configv1.TLSGroupSecP256r1MLKEM768:  cryptotls.SecP256r1MLKEM768,
	configv1.TLSGroupSecP384r1MLKEM1024: cryptotls.SecP384r1MLKEM1024,
}

// usableGroups converts TLS group names to CurveIDs this Go runtime supports.
// Unknown names are reported as unsupported; a non-empty request with no
// usable groups is an error.
func usableGroups(groups []configv1.TLSGroup) ([]cryptotls.CurveID, []string) {
	var curveIDs []cryptotls.CurveID
	seen := make(map[cryptotls.CurveID]struct{}, len(groups))
	var unsupported []string
	for _, name := range groups {
		id, ok := tlsGroupToCurveID[name]
		if !ok {
			unsupported = append(unsupported, string(name))
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		curveIDs = append(curveIDs, id)
	}
	return curveIDs, unsupported
}

// hasTLS12Group reports whether at least one resolved group can be used by a
// TLS 1.2 (or lower) handshake. The ML-KEM hybrid groups are TLS 1.3-only.
func hasTLS12Group(curves []cryptotls.CurveID) bool {
	for _, id := range curves {
		switch id {
		case cryptotls.X25519MLKEM768, cryptotls.SecP256r1MLKEM768, cryptotls.SecP384r1MLKEM1024:
			continue
		default:
			return true
		}
	}
	return false
}

// parseSuites validates a profile spec: the minimum version must be known and
// non-empty, and the requested cipher list must contain at least one suite
// usable by this Go runtime at that minimum. Requested groups must contain at
// least one recognized group, and at least one usable for the configured
// minimum version. TLS 1.3 cipher selection stays runtime-managed (no suites);
// TLS 1.3-only suites requested for lower minimums are unusable.
func parseSuites(
	spec *configv1.TLSProfileSpec, rawSpec map[string]interface{},
) (uint16, []uint16, []cryptotls.CurveID, map[string]interface{}, error) {
	if spec == nil {
		return 0, nil, nil, nil, fmt.Errorf("TLS profile spec is missing")
	}

	minVersion, err := tlsVersion(spec.MinTLSVersion)
	if err != nil {
		return 0, nil, nil, nil, err
	}

	var ciphers []uint16
	if minVersion != cryptotls.VersionTLS13 {
		if len(spec.Ciphers) == 0 {
			return 0, nil, nil, nil,
				fmt.Errorf("TLS profile with minimum %s has no cipher suites", spec.MinTLSVersion)
		}

		var unsupported []string
		ciphers, unsupported = usableCiphers(spec.Ciphers)
		if len(unsupported) > 0 {
			profileLog.Info("Dropping TLS profile ciphers not usable by this Go runtime", "dropped", unsupported)
		}
		if len(ciphers) == 0 {
			return 0, nil, nil, nil, fmt.Errorf(
				"TLS profile with minimum %s has no cipher suites usable by this Go runtime: %v",
				spec.MinTLSVersion, spec.Ciphers)
		}
	}

	curveIDs, unsupportedGroups := usableGroups(spec.Groups)
	if len(unsupportedGroups) > 0 {
		profileLog.Info("Dropping unrecognized TLS profile groups", "dropped", unsupportedGroups)
	}
	if len(spec.Groups) > 0 && len(curveIDs) == 0 {
		return 0, nil, nil, nil, fmt.Errorf("TLS profile has no usable groups")
	}
	if minVersion != cryptotls.VersionTLS13 && len(curveIDs) > 0 && !hasTLS12Group(curveIDs) {
		return 0, nil, nil, nil, fmt.Errorf(
			"TLS profile with minimum %s has no groups usable at that version: %v",
			spec.MinTLSVersion, spec.Groups)
	}
	return minVersion, ciphers, curveIDs, rawSpec, nil
}

func tlsVersion(v configv1.TLSProtocolVersion) (uint16, error) {
	switch v {
	case configv1.VersionTLS10:
		return cryptotls.VersionTLS10, nil
	case configv1.VersionTLS11:
		return cryptotls.VersionTLS11, nil
	case configv1.VersionTLS12:
		return cryptotls.VersionTLS12, nil
	case configv1.VersionTLS13:
		return cryptotls.VersionTLS13, nil
	case "":
		return 0, fmt.Errorf("TLS profile has no minimum version")
	default:
		return 0, fmt.Errorf("unknown minimum TLS version %q", v)
	}
}

// usableCiphers translates IANA or OpenSSL cipher names to the IDs this Go
// runtime can negotiate at TLS 1.2 or below. TLS 1.3-only suites, unknown
// names, and suites the runtime does not implement are reported as unusable.
func usableCiphers(names []string) ([]uint16, []string) {
	implemented := make(map[uint16]struct{})
	for _, suite := range cryptotls.CipherSuites() {
		implemented[suite.ID] = struct{}{}
	}
	for _, suite := range cryptotls.InsecureCipherSuites() {
		implemented[suite.ID] = struct{}{}
	}

	ciphers := make([]uint16, 0, len(names))
	seen := make(map[uint16]struct{}, len(names))
	var unsupported []string
	for _, name := range names {
		id, ok := cipherID(name)
		if !ok {
			unsupported = append(unsupported, name)
			continue
		}
		// TLS 1.3 suite IDs are not configurable below TLS 1.3.
		if id >= 0x1301 && id <= 0x1303 {
			unsupported = append(unsupported, name)
			continue
		}
		if _, ok := implemented[id]; !ok {
			unsupported = append(unsupported, name)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ciphers = append(ciphers, id)
	}
	return ciphers, unsupported
}

// cipherID resolves a cipher name, accepting both IANA and OpenSSL
// (library-go) spellings.
func cipherID(name string) (uint16, bool) {
	if id, err := ocpcrypto.CipherSuite(name); err == nil {
		return id, true
	}

	ianaNames := ocpcrypto.OpenSSLToIANACipherSuites([]string{name})
	if len(ianaNames) == 1 {
		if id, err := ocpcrypto.CipherSuite(ianaNames[0]); err == nil {
			return id, true
		}
	}

	return 0, false
}

func validateCustomCipherSuites(names []string) error {
	for _, name := range names {
		normalizedName := strings.ToUpper(name)
		canonicalName := normalizedName
		if id, ok := cipherID(normalizedName); ok {
			canonicalName = cryptotls.CipherSuiteName(id)
		}
		if ianaNames := ocpcrypto.OpenSSLToIANACipherSuites([]string{normalizedName}); len(ianaNames) == 1 {
			canonicalName = ianaNames[0]
		}
		canonicalName = strings.ToUpper(canonicalName)
		if strings.Contains(canonicalName, "RC4") ||
			strings.Contains(canonicalName, "3DES") ||
			strings.Contains(canonicalName, "CBC3") ||
			strings.HasSuffix(canonicalName, "_CBC_SHA") {
			return fmt.Errorf("custom TLS profile contains disallowed cipher suite %q", name)
		}
	}
	return nil
}

// NextProtos advertises h2 when the cipher list supports HTTP/2. net/http
// rejects HTTP/2 connections that negotiate below TLS 1.2.
func NextProtos(_ uint16, ciphers []uint16) []string {
	if servesHTTP2(ciphers) {
		return []string{"h2", "http/1.1"}
	}
	return []string{"http/1.1"}
}

func servesHTTP2(ciphers []uint16) bool {
	if len(ciphers) == 0 {
		// Go's default suites include AES-128-GCM.
		return true
	}
	for _, id := range ciphers {
		if id == cryptotls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 ||
			id == cryptotls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
			return true
		}
	}
	return false
}
