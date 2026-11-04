package aws

import "log/slog"

// secretMaterial separates stored value bytes from metadata DTOs. Generic
// formatting, structured logging and JSON logging must not reveal its content.
// The eventual authorized GetSecretValue writer must explicitly select raw only
// after same-transaction ARN resolution and authorization. This is not encryption.
type secretMaterial struct {
	// fmt does not call methods on unexported fields inside another struct.
	// Keep only an indirection here, so that fallback formatting exposes an
	// address rather than recursively printing the underlying string bytes.
	raw *string
}

func newSecretMaterial(value string) secretMaterial { return secretMaterial{raw: &value} }

// plaintext is an explicit escape hatch for validated request comparison and
// an authorized Get response. It must never be passed to a logger or metadata.
func (s secretMaterial) plaintext() string {
	if s.raw == nil { return "" }
	return *s.raw
}

func (secretMaterial) String() string {
	return "[REDACTED]"
}

func (secretMaterial) GoString() string {
	return "[REDACTED]"
}

func (secretMaterial) LogValue() slog.Value {
	return slog.StringValue("[REDACTED]")
}

func (secretMaterial) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

// SecretMetadata never owns a value. The AWS Describe and List response maps
// deliberately use different JSON field names for labeled versions.
type SecretMetadata struct {
	ARN             string  `json:"ARN"`
	Name            string  `json:"Name"`
	Description     string  `json:"Description,omitempty"`
	CreatedDate     float64 `json:"CreatedDate"`
	LastChangedDate float64 `json:"LastChangedDate"`
}

type SecretDescription struct {
	SecretMetadata
	VersionIdsToStages map[string][]string `json:"VersionIdsToStages"`
}

type SecretListEntry struct {
	SecretMetadata
	SecretVersionsToStages map[string][]string `json:"SecretVersionsToStages"`
}

// SecretAuthorizer must capture an already loaded immutable policy snapshot.
// It is invoked inside a short SQL transaction and must not access the database,
// wait for guest code, perform I/O, or convert policy failures to an ordinary Deny.
type SecretAuthorizer func(action, arn string) error
