# Provider URL path routing

The registry strips a literal `/_simulator/providers/<registered-id>` prefix from
`URL.Path` and retains the corresponding escaped tail in `URL.RawPath`.
`URL.EscapedPath()` is the leaf protocol's source for spelling-sensitive path
validation. The decoded `Path` still follows Go's single URL parse; there is no
second decode, path cleaning, dot-segment collapse or redirect in the registry.
Default-provider requests are not rewritten. Both a provider root with no final
slash and the explicit slash route to `/`.

Provider identifiers and their separating slash must be literal routing syntax.
Encoded identifiers or encoded delimiters are rejected with 404. This is an
intentional compatibility tightening: older routing could accept an encoded
registered identifier or delimiter after decoding. Literal registered provider
IDs, default routes and their root slash behavior are retained. Encodings in the
leaf-owned tail, including `%2F`, `%252F`, Unicode, dot segments and repeated
slashes, remain available for the leaf's own policy. An Azure-like protocol can
reject encoded container separators while an object-key protocol can assign its
own semantics. This is not evidence of a current S3 defect or Azure/GCP support.

The cloned request retains authentication context, headers, method, query/body
and the original `RequestURI`; callers must not mistake that original prefixed
request target for the leaf-local URL. The original request URL is not mutated.
No auth authority is introduced by selecting a provider name.

The dedicated tests use actual loopback HTTP with independent map-backed probe
leaves to check escaped-target fidelity and thirteen colliding-looking keys.
These are registry-contract tests, not real cloud APIs, SQL isolation, AWS SDK,
Azure implementation acceptance or an authenticated AWS console comparison.
Required full integration/E2E checks remain separate from focused native tests.
