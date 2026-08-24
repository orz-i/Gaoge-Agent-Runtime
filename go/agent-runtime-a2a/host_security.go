package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

var (
	ErrHostedUnauthenticated = errors.New("A2A hosted request is unauthenticated")
	ErrHostedUnauthorized    = errors.New("A2A hosted request is unauthorized")
)

// HostedSecuritySchemeKind is the bounded set of public Agent Card schemes
// currently emitted by the HTTP+JSON host edge.
type HostedSecuritySchemeKind string

const (
	HostedSecurityAPIKey        HostedSecuritySchemeKind = "api_key"
	HostedSecurityHTTP          HostedSecuritySchemeKind = "http"
	HostedSecurityMutualTLS     HostedSecuritySchemeKind = "mutual_tls"
	HostedSecurityOpenIDConnect HostedSecuritySchemeKind = "open_id_connect"
)

// HostedSecurityScheme is a host-neutral public security declaration.
type HostedSecurityScheme struct {
	Name             string
	Kind             HostedSecuritySchemeKind
	Description      string
	ParameterName    string
	Location         string
	Scheme           string
	BearerFormat     string
	OpenIDConnectURL string
}

// HostedSecurityRequirement is one OR option whose Schemes are jointly required.
type HostedSecurityRequirement struct {
	Schemes map[string][]string
}

// HostedCall is the bounded request metadata supplied to host authentication.
type HostedCall struct {
	Method  string
	Tenant  string
	Headers http.Header
}

// HostedPrincipal is the authenticated product identity. Attributes are
// request-scoped JSON and are never copied into task records or Agent Cards.
type HostedPrincipal struct {
	Subject    string
	Tenant     string
	Attributes json.RawMessage
}

// HostedAuthenticator authenticates one A2A call using host-owned credentials.
type HostedAuthenticator interface {
	Authenticate(context.Context, HostedCall) (HostedPrincipal, error)
}

// HostedAuthenticatorFunc adapts one host authentication function.
type HostedAuthenticatorFunc func(context.Context, HostedCall) (HostedPrincipal, error)

func (authenticator HostedAuthenticatorFunc) Authenticate(
	ctx context.Context,
	call HostedCall,
) (HostedPrincipal, error) {
	if authenticator == nil {
		return HostedPrincipal{}, ErrHostedUnauthenticated
	}
	return authenticator(ctx, call)
}

type hostedPrincipalContextKey struct{}

const (
	hostedPrincipalSubjectAttribute = "gaoge.a2a.subject"
	hostedPrincipalTenantAttribute  = "gaoge.a2a.tenant"
)

type hostAuthenticationInterceptor struct {
	authenticator HostedAuthenticator
	allowedTenant string
}

func (interceptor hostAuthenticationInterceptor) Before(
	ctx context.Context,
	callContext *a2asrv.CallContext,
	_ *a2asrv.Request,
) (context.Context, any, error) {
	if callContext == nil {
		return ctx, nil, a2asdk.ErrUnauthenticated
	}
	call := HostedCall{
		Method: callContext.Method(), Tenant: strings.TrimSpace(callContext.Tenant()),
		Headers: hostedCallHeaders(callContext.ServiceParams()),
	}
	principal, err := authenticateHostedPrincipal(ctx, interceptor.authenticator, interceptor.allowedTenant, call)
	if err != nil {
		return ctx, nil, err
	}
	callContext.User = a2asrv.NewAuthenticatedUser(hostedOwnerKey(principal), map[string]any{
		hostedPrincipalSubjectAttribute: principal.Subject,
		hostedPrincipalTenantAttribute:  principal.Tenant,
	})
	return context.WithValue(ctx, hostedPrincipalContextKey{}, principal), nil, nil
}

func authenticateHostedPrincipal(
	ctx context.Context,
	authenticator HostedAuthenticator,
	allowedTenant string,
	call HostedCall,
) (HostedPrincipal, error) {
	call.Tenant = strings.TrimSpace(call.Tenant)
	if authenticator == nil {
		return HostedPrincipal{}, a2asdk.ErrUnauthenticated
	}
	if strings.TrimSpace(allowedTenant) != "" && call.Tenant != strings.TrimSpace(allowedTenant) {
		return HostedPrincipal{}, a2asdk.ErrUnauthorized
	}
	principal, err := authenticator.Authenticate(ctx, call)
	if err != nil {
		return HostedPrincipal{}, a2asdk.ErrUnauthenticated
	}
	principal = cloneHostedPrincipal(principal)
	if !validPrincipalValue(principal.Subject) || !validOptionalPrincipalValue(principal.Tenant) {
		return HostedPrincipal{}, a2asdk.ErrUnauthenticated
	}
	if len(principal.Attributes) > 0 {
		if _, err = decodeMetadata(principal.Attributes); err != nil {
			return HostedPrincipal{}, a2asdk.ErrUnauthenticated
		}
	}
	if call.Tenant != "" && principal.Tenant != "" && call.Tenant != principal.Tenant {
		return HostedPrincipal{}, a2asdk.ErrUnauthorized
	}
	if call.Tenant != "" {
		principal.Tenant = call.Tenant
	}
	return principal, nil
}

func (hostAuthenticationInterceptor) After(
	context.Context,
	*a2asrv.CallContext,
	*a2asrv.Response,
) error {
	return nil
}

func hostedCallHeaders(params *a2asrv.ServiceParams) http.Header {
	headers := make(http.Header)
	if params == nil {
		return headers
	}
	for key, values := range params.List() {
		headers[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	return headers
}

func hostedPrincipalFromContext(ctx context.Context) HostedPrincipal {
	if ctx == nil {
		return HostedPrincipal{}
	}
	principal, _ := ctx.Value(hostedPrincipalContextKey{}).(HostedPrincipal)
	if validPrincipalValue(principal.Subject) {
		return cloneHostedPrincipal(principal)
	}
	if callContext, ok := a2asrv.CallContextFrom(ctx); ok && callContext.User != nil && callContext.User.Authenticated {
		subject, _ := callContext.User.Attributes[hostedPrincipalSubjectAttribute].(string)
		tenant, _ := callContext.User.Attributes[hostedPrincipalTenantAttribute].(string)
		principal = HostedPrincipal{Subject: subject, Tenant: tenant}
	}
	return cloneHostedPrincipal(principal)
}

func cloneHostedPrincipal(principal HostedPrincipal) HostedPrincipal {
	principal.Subject = strings.TrimSpace(principal.Subject)
	principal.Tenant = strings.TrimSpace(principal.Tenant)
	principal.Attributes = append(json.RawMessage(nil), principal.Attributes...)
	return principal
}

func hostedOwnerKey(principal HostedPrincipal) string {
	return principal.Tenant + "\x00" + principal.Subject
}

func validPrincipalValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n")
}

func validOptionalPrincipalValue(value string) bool {
	return strings.TrimSpace(value) == "" || validPrincipalValue(value)
}

func projectHostedSecurity(card HostedCard, projected *a2asdk.AgentCard) error {
	if projected == nil || len(card.SecuritySchemes) > maxSecuritySchemes ||
		len(card.SecurityRequirements) > maxSecuritySchemes || len(card.Signatures) > maxCardSignatures {
		return ErrInvalidHost
	}
	if len(card.SecuritySchemes) == 0 && len(card.SecurityRequirements) == 0 && len(card.Signatures) == 0 {
		return nil
	}
	projected.SecuritySchemes = make(a2asdk.NamedSecuritySchemes, len(card.SecuritySchemes))
	for _, scheme := range card.SecuritySchemes {
		name := strings.TrimSpace(scheme.Name)
		if _, exists := projected.SecuritySchemes[a2asdk.SecuritySchemeName(name)]; exists {
			return ErrInvalidHost
		}
		protocolScheme, err := toProtocolSecurityScheme(scheme)
		if err != nil {
			return err
		}
		projected.SecuritySchemes[a2asdk.SecuritySchemeName(name)] = protocolScheme
	}
	projected.SecurityRequirements = make(a2asdk.SecurityRequirementsOptions, 0, len(card.SecurityRequirements))
	for _, requirement := range card.SecurityRequirements {
		if len(requirement.Schemes) == 0 || len(requirement.Schemes) > maxSecuritySchemes {
			return ErrInvalidHost
		}
		protocolRequirement := make(a2asdk.SecurityRequirements, len(requirement.Schemes))
		for rawName, rawScopes := range requirement.Schemes {
			name := strings.TrimSpace(rawName)
			if _, declared := projected.SecuritySchemes[a2asdk.SecuritySchemeName(name)]; !declared ||
				len(rawScopes) > maxAgentSkillItems {
				return ErrInvalidHost
			}
			scopes := make(a2asdk.SecuritySchemeScopes, 0, len(rawScopes))
			for _, scope := range rawScopes {
				if !validRemoteText(scope, true) {
					return ErrInvalidHost
				}
				scopes = append(scopes, strings.TrimSpace(scope))
			}
			protocolRequirement[a2asdk.SecuritySchemeName(name)] = scopes
		}
		projected.SecurityRequirements = append(projected.SecurityRequirements, protocolRequirement)
	}
	projected.Signatures = make([]a2asdk.AgentCardSignature, 0, len(card.Signatures))
	for _, signature := range card.Signatures {
		if !validRemoteText(signature.Protected, true) || !validRemoteText(signature.Signature, true) {
			return ErrInvalidHost
		}
		projected.Signatures = append(projected.Signatures, a2asdk.AgentCardSignature{
			Protected: strings.TrimSpace(signature.Protected), Signature: strings.TrimSpace(signature.Signature),
		})
	}
	return nil
}

func toProtocolSecurityScheme(scheme HostedSecurityScheme) (a2asdk.SecurityScheme, error) {
	if !validRemoteText(scheme.Name, true) || !validRemoteText(scheme.Description, false) {
		return nil, ErrInvalidHost
	}
	description := strings.TrimSpace(scheme.Description)
	switch scheme.Kind {
	case HostedSecurityAPIKey:
		location := a2asdk.APIKeySecuritySchemeLocation(strings.TrimSpace(scheme.Location))
		if !validRemoteText(scheme.ParameterName, true) ||
			(location != a2asdk.APIKeySecuritySchemeLocationHeader &&
				location != a2asdk.APIKeySecuritySchemeLocationQuery &&
				location != a2asdk.APIKeySecuritySchemeLocationCookie) {
			return nil, ErrInvalidHost
		}
		return a2asdk.APIKeySecurityScheme{
			Description: description, Name: strings.TrimSpace(scheme.ParameterName), Location: location,
		}, nil
	case HostedSecurityHTTP:
		if !validRemoteText(scheme.Scheme, true) || !validRemoteText(scheme.BearerFormat, false) {
			return nil, ErrInvalidHost
		}
		return a2asdk.HTTPAuthSecurityScheme{
			Description: description, Scheme: strings.TrimSpace(scheme.Scheme),
			BearerFormat: strings.TrimSpace(scheme.BearerFormat),
		}, nil
	case HostedSecurityMutualTLS:
		return a2asdk.MutualTLSSecurityScheme{Description: description}, nil
	case HostedSecurityOpenIDConnect:
		if !validRemoteText(scheme.OpenIDConnectURL, true) {
			return nil, ErrInvalidHost
		}
		return a2asdk.OpenIDConnectSecurityScheme{
			Description: description, OpenIDConnectURL: strings.TrimSpace(scheme.OpenIDConnectURL),
		}, nil
	default:
		return nil, ErrInvalidHost
	}
}
