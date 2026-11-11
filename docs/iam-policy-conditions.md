# Bounded string conditions

The identity-policy evaluator supports `StringEquals` and `StringLike` for
single-valued provider context: `aws:userid`, `aws:ResourceTag/<key>` and
`ssm:resourceTag/<key>`. Multiple operators and keys are ANDed; values for one
key are alternatives. Values are case-sensitive; context key names are
case-insensitive. `StringLike` supports `*` and `?`; `StringEquals` is literal.
Unknown operators/keys, null values, duplicate clauses and unsupported variables
are rejected. This is not complete AWS IAM condition support.

`EvaluateWithContext` receives an immutable provider-derived context. Providers
must read resource tags from their own persisted state and user/session identity
from authenticated session state, in the same authorization snapshot as the
operation. Request headers, body fields and guest claims are not trusted context.
The legacy `Evaluate` supplies nil context and rejects a policy containing any
condition or variable. This prevents an unevaluated conditional Deny from being
dropped beside an unconditional Allow. A non-nil empty context explicitly means
the owning provider established that no supported keys apply; absent keys then
do not match the positive string predicates.
Parsing a condition does not establish that every API supplies its context.

`${aws:userid}` is the only supported variable in resource and condition values.
It requires an available, nonempty user identity and is substituted as literal
data, never as injected wildcard syntax. Variables are not accepted in Action.
An unresolved variable never grants access. Explicit matching Deny still wins.
The evaluator's context map is a caller contract, not an authentication mechanism.

Official behavior references:
- https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html
- https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_variables.html

Focused tests cover absent keys, exact and wildcard values, AND/OR combinations,
explicit deny, template-style session correlation, unresolved variables, null and
duplicate restrictions. Provider integration and end-to-end participant access
must be tested separately; evaluator tests are not a Battle acceptance result.
