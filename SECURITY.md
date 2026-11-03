# Security policy

Report vulnerabilities privately through [GitHub private vulnerability reporting](https://github.com/susumutomita/TenkaCloudSimulator/security/advisories/new) or oyster880@gmail.com. Include the version, provider, operation, sanitized request sequence, impact, and reproduction steps. Do not send real credentials or participant secrets.

This increment is an independent local simulator, not a multi-tenant hosted security boundary. It creates no real cloud infrastructure and runs no user code. Default numeric loopback binding requires explicit --allow-remote to expose a network listener. Use a trusted isolated environment; simulator principal selection and administrator policy APIs are intentionally not connection authentication.

Core invariants: no outbound cloud calls; no real credentials; same-origin console/API reference; bounded object payloads; exact bytes shared between API/UI; transactionally enforced parent relationships and conditional writes; explicit IAM Deny precedence for the declared policy subset; unsupported operations/policy fields fail; database errors do not become successful empty results. Uploaded objects download as attachments and are not rendered as console HTML.

See [runtime design and limitations](docs/design/0001-go-standalone.md) and [supported operations](README.md). The legacy workload/container/world security claims do not apply to the replacement runtime because those features are absent. Never use this limited IAM model as a production authorization engine.
