# Security policy

Report privately through [GitHub private vulnerability reporting](https://github.com/susumutomita/TenkaCloudSimulator/security/advisories/new) or oyster880@gmail.com. Include sanitized version, request sequence, impact and reproduction; never real credentials or participant flags.

The CLI requires opaque access tokens by default. Operator tokens are stored in a private regular file; participant tokens are random, stored hashed in memory, bound to one exercise identity, and expire after 15 minutes. Restart revokes participant sessions. Browser cookies are HttpOnly/SameSite Strict and process-specific; HTTPS adds Secure. Local HTTP requires a trusted machine. There is no persistent session revocation/logout endpoint yet.

A host must allocate a separate process, SQLite file and operator authority per team, authenticate users and authorize proxy routes. Loopback alone does not authorize a participant. Tokens cannot be reused between processes. Never give participants operator tokens, host DB files or private CloudFormation outputs. server.New is a trusted unauthenticated composition API; callers must wrap server.NewAccess. Selecting an IAM principal is exercise context, never tenant authentication.

Numeric loopback binding is default; --allow-remote is only for a trusted isolated network/proxy and does not provide TLS. --unsafe-no-auth is explicitly development-only and cannot accompany remote binding. Host and same-origin checks reject DNS rebinding/cross-site API requests. The runtime makes no cloud requests, runs no catalog code, fetches no TemplateURL, uses no real AWS credentials and displays no object HTML.

Invariants: exact object bytes; bounded requests/state; restrictive SQLite FKs and atomic CFN resource creation/deletion; ownership reservations survive manual resource deletion; unsupported resources/options fail before mutation; explicit IAM Deny; participant token cannot select admin, manage policies or read stack outputs; storage failures cannot become successful empty results. Finite TLC models and integration tests cover selected invariants, not a full implementation proof.

IAM is a limited local identity-policy evaluator. No STS, AWS federation, trust/managed/resource-policy permissions, Condition, KMS, SCP or production cloud authentication. Synthetic account ARNs use 000000000000. Isolation does not protect against a malicious process under the same OS account, a compromised operator, or external SQLite writers. See [design](docs/design/0001-go-standalone.md) and [catalog limits](docs/catalog.md).
