// Package provider defines non-secret, immutable provider identities.
//
// A runtime executable is not a provider identity: for example, the Claude
// Code runtime can be configured to call Anthropic or Z.ai. Callers use an
// Identity value to bind accounting and admission to the actual provider
// configuration instead of the executable family.
package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var identityPartPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var configSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Identity is a normalized provider boundary. Its fields are deliberately
// private: construct it with NewIdentity and pass it by value. The hash is
// safe to persist, while the identity itself never accepts a credential,
// query string, URL fragment, or URL userinfo.
type Identity struct {
	provider       string
	accountAlias   string
	model          string
	endpoint       string
	runtime        string
	configSHA256   string
	identitySHA256 string
}

// IdentityEvidenceGrade states how much of an identity tuple has been
// independently observed. A profile-attested identity is immutable and safe
// to use as a routing/admission boundary, but it is not proof that an opaque
// runtime is currently honoring every endpoint/config setting in that tuple.
//
// Do not promote ProfileAttested to RuntimeVerified from a process launch,
// pane title, or model name alone.
type IdentityEvidenceGrade string

const (
	IdentityEvidenceUnverified      IdentityEvidenceGrade = "unverified"
	IdentityEvidenceProfileAttested IdentityEvidenceGrade = "profile_attested"
	IdentityEvidenceRuntimeVerified IdentityEvidenceGrade = "runtime_verified"
)

// NewIdentity validates and normalizes the complete provider tuple. endpoint
// must be an HTTPS URL without credential-bearing components. configSHA256 is
// the hash of a separately redacted configuration manifest, never a hash of a
// secret value.
func NewIdentity(providerName, accountAlias, model, endpoint, runtime, configSHA256 string) (Identity, error) {
	providerName, err := normalizePart("provider", providerName)
	if err != nil {
		return Identity{}, err
	}
	accountAlias, err = normalizePart("account alias", accountAlias)
	if err != nil {
		return Identity{}, err
	}
	runtime, err = normalizePart("runtime", runtime)
	if err != nil {
		return Identity{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" || hasControl(model) {
		return Identity{}, fmt.Errorf("model must be non-empty and contain no control characters")
	}
	configSHA256 = strings.ToLower(strings.TrimSpace(configSHA256))
	if !configSHA256Pattern.MatchString(configSHA256) {
		return Identity{}, fmt.Errorf("config SHA-256 must be exactly 64 lowercase hexadecimal characters")
	}
	endpoint, err = normalizeEndpoint(endpoint)
	if err != nil {
		return Identity{}, err
	}

	id := Identity{
		provider:     providerName,
		accountAlias: accountAlias,
		model:        model,
		endpoint:     endpoint,
		runtime:      runtime,
		configSHA256: configSHA256,
	}
	id.identitySHA256 = hashFields(id.provider, id.accountAlias, id.model, id.endpoint, id.runtime, id.configSHA256)
	return id, nil
}

func normalizePart(name, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !identityPartPattern.MatchString(value) {
		return "", fmt.Errorf("%s must match %s", name, identityPartPattern.String())
	}
	return value, nil
}

func normalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", fmt.Errorf("endpoint must be an absolute HTTPS URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("endpoint must not contain userinfo, query, or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("endpoint host is required")
	}
	port := u.Port()
	if port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	p := path.Clean("/" + strings.TrimPrefix(u.EscapedPath(), "/"))
	if p == "." {
		p = "/"
	}
	return "https://" + host + p, nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func hashFields(fields ...string) string {
	h := sha256.New()
	for _, field := range fields {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (i Identity) Provider() string     { return i.provider }
func (i Identity) AccountAlias() string { return i.accountAlias }
func (i Identity) Model() string        { return i.model }
func (i Identity) Endpoint() string     { return i.endpoint }
func (i Identity) Runtime() string      { return i.runtime }
func (i Identity) ConfigSHA256() string { return i.configSHA256 }
func (i Identity) Hash() string         { return i.identitySHA256 }

// EvidenceGrade reports the evidence available from a configured immutable
// tuple alone. Runtime-specific adapters may publish stronger, separately
// reviewed observations, but construction of Identity never claims them.
func (i Identity) EvidenceGrade() IdentityEvidenceGrade {
	if !i.Valid() {
		return IdentityEvidenceUnverified
	}
	return IdentityEvidenceProfileAttested
}

// Valid reports whether the value was produced by NewIdentity. The zero value
// is deliberately invalid so admission and accounting cannot collapse
// malformed callers into a shared empty capacity scope.
func (i Identity) Valid() bool {
	return configSHA256Pattern.MatchString(i.identitySHA256)
}

// CapacityScope derives an identity-specific admission key. It is intentionally
// opaque so callers cannot collapse accounts or models by hand.
func (i Identity) CapacityScope() CapacityScope {
	if !i.Valid() {
		return ""
	}
	return CapacityScope("provider:" + i.identitySHA256)
}

// CapacityScope is the stable, non-secret key for a provider/account/model
// admission bucket.
type CapacityScope string

func (s CapacityScope) String() string { return string(s) }
