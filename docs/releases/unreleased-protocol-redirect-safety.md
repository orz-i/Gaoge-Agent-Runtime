# Unreleased upgrade note — MCP/A2A redirect safety

This change targets the next immutable prerelease. It does not publish a release
or change an existing tag.

## Changed behavior

The MCP and A2A adapters reject HTTP 301, 302, 303, 307 and 308 redirects,
including redirects within the same origin. A redirect cannot cause a request or
body replay to its target, or reattach the original endpoint's credentials to
that target. Protocol discovery or execution returns an error instead. Protocol
negotiation may still send multiple requests to the original validated endpoint.

The adapters copy the supplied `http.Client`. Its existing `CheckRedirect`
callback is consulted when a redirect is encountered: a non-nil error is
preserved; an allow decision still results in `http.ErrUseLastResponse` and no
request to the redirect target. The host client itself and its transport, TLS, proxy, timeout
and cookie-jar configuration remain unchanged. There is no redirect opt-out or
compatibility fallback.

## Required host action

1. Configure the final MCP endpoint and A2A discovery/interface URLs directly.
   Remove redirect-dependent aliases, trailing-slash normalization and
   HTTP-to-HTTPS upgrade hops from the configured protocol path.
2. Authorize the final endpoint through the host's endpoint and DNS/IP/egress
   policy before binding it. Do not automatically trust a remote `Location`
   header or reuse credentials across origins.
3. Review in-flight work whose frozen endpoint depends on a redirect. A2A shadow
   runs retain their immutable binding; reconcile or restart affected work with
   an explicitly authorized final binding. Do not rewrite persisted bindings or
   replay an uncertain external effect solely because its previous call failed.
4. Keep host network controls in place. Redirect refusal does not replace DNS/IP
   validation or the security responsibilities of a host-supplied transport.

No exported Go API, HTTP v1 shape, Kernel lifecycle, persistence schema, dependency
version or migration changes. Deploy the fix through the normal coordinated
module release process.

## Verification boundary

The regression suites exercise real public MCP/A2A client operations against
loopback HTTP servers with fake authentication. They cover redirect status codes,
cross-host and same-origin destinations, retained host deny callbacks, denied
host allow callbacks, and independent reuse of the original host HTTP client.
Existing direct protocol discovery/execution tests continue to verify normal
non-redirected operation. These checks make no claim of live third-party endpoint
compatibility or production network isolation.
