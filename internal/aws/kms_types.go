// Copyright (c) 2025 sivchari
// SPDX-License-Identifier: MIT
// Selected metadata wire fields adapted from Kumo ce37a8fb406ecb96b5619204f645c26302b651a5,
// internal/service/kms/types.go. Original notice: third_party/KUMO-LICENSE.txt.
// Strict validation, private SQLite storage and authenticated local blob format are local adapters.
package aws

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const kmsAccount = "000000000000"
const kmsDefaultPolicy = `{"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"kms:*","Resource":"*"}]}`
const kmsMaxEncryptions int64 = 1 << 32

var kmsUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// KMSKeyMetadata is reconstructed from provider-owned SQLite columns. The local
// symmetric-key subset is explicitly projected into the AWS KeyMetadata shape;
// unsupported AWS fields and states are not fabricated.
type KMSKeyMetadata struct {
	AWSAccountID         string
	KeyID                string
	Arn                  string
	CreationDate         float64
	Enabled              bool
	Description          string
	KeyUsage             string
	KeyState             string
	Origin               string
	KeyManager           string
	KeySpec              string
	EncryptionAlgorithms []string
	MultiRegion          bool
}

// https://docs.aws.amazon.com/kms/latest/APIReference/API_KeyMetadata.html
// Emit the established local subset, including false/zero/empty fields and a
// null algorithm list when nil. The numeric timestamp remains JSON seconds.
func (key KMSKeyMetadata) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"AWSAccountId", key.AWSAccountID, false},
		awsOwnedWireField{"KeyId", key.KeyID, false},
		awsOwnedWireField{"Arn", key.Arn, false},
		awsOwnedWireField{"CreationDate", key.CreationDate, false},
		awsOwnedWireField{"Enabled", key.Enabled, false},
		awsOwnedWireField{"Description", key.Description, false},
		awsOwnedWireField{"KeyUsage", key.KeyUsage, false},
		awsOwnedWireField{"KeyState", key.KeyState, false},
		awsOwnedWireField{"Origin", key.Origin, false},
		awsOwnedWireField{"KeyManager", key.KeyManager, false},
		awsOwnedWireField{"KeySpec", key.KeySpec, false},
		awsOwnedWireField{"EncryptionAlgorithms", key.EncryptionAlgorithms, false},
		awsOwnedWireField{"MultiRegion", key.MultiRegion, false})
}

type kmsError struct{ code, message string }

func (e kmsError) Error() string            { return e.message }
func kmsFailure(code, message string) error { return kmsError{code, message} }
func kmsInvalid() error {
	return kmsFailure("ValidationException", "Invalid or unsupported KMS request fields.")
}
func kmsInternal() error {
	return kmsFailure("KMSInternalException", "Local KMS state operation failed.")
}
func kmsARN(region, id string) string {
	return "arn:aws:kms:" + region + ":" + kmsAccount + ":key/" + id
}
func kmsID(region, value string) (string, error) {
	if !kmsRegion(region) {
		return "", kmsInvalid()
	}
	if strings.HasPrefix(value, "arn:") {
		value = strings.TrimPrefix(value, "arn:aws:kms:"+region+":"+kmsAccount+":key/")
	}
	if !kmsUUID.MatchString(value) {
		return "", kmsInvalid()
	}
	return value, nil
}
func kmsRegion(region string) bool { return slices.Contains(Regions, region) }
func kmsDescription(s string) bool { return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 8192 }
func kmsContext(context map[string]string) ([]byte, error) {
	if len(context) > 100 {
		return nil, kmsInvalid()
	}
	if context == nil {
		context = map[string]string{}
	}
	for k, v := range context {
		if k == "" || !utf8.ValidString(k) || !utf8.ValidString(v) {
			return nil, kmsInvalid()
		}
	}
	b, err := json.Marshal(context)
	if err != nil || len(b) > 8192 {
		return nil, kmsInvalid()
	}
	return b, nil
}
func kmsAAD(region, id string, context map[string]string) ([]byte, error) {
	c, err := kmsContext(context)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Format, Region, Key string
		Context             json.RawMessage
	}{"TCSIMKMS1", region, kmsARN(region, id), c})
}
func kmsMetadata(region, id, description, state string, created float64) KMSKeyMetadata {
	return KMSKeyMetadata{kmsAccount, id, kmsARN(region, id), created, state == "Enabled", description, "ENCRYPT_DECRYPT", state, "AWS_KMS", "CUSTOMER", "SYMMETRIC_DEFAULT", []string{"SYMMETRIC_DEFAULT"}, false}
}
func kmsCorrupt(reason string) error { return fmt.Errorf("invalid persisted KMS %s", reason) }
