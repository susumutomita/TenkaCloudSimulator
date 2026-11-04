package aws

import (
	"crypto/aes"
	"crypto/cipher"
)

// This versioned simulator format is deliberately not an AWS portable ciphertext.
const kmsBlobPrefix = "TCSIMKMS1"

func kmsSeal(material []byte, region, id string, context map[string]string, plaintext []byte) ([]byte, error) {
	if len(material) != 32 {
		return nil, kmsInternal()
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, kmsInternal()
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, kmsInternal()
	}
	aad, err := kmsAAD(region, id, context)
	if err != nil {
		return nil, err
	}
	header := []byte(kmsBlobPrefix + string(byte(len(region))) + region + id)
	return aead.Seal(header, nil, plaintext, aad), nil
}
func kmsBlob(blob []byte) (region, id string, encrypted []byte, err error) {
	invalid := kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request.")
	prefix := len(kmsBlobPrefix)
	if len(blob) < prefix+1 || len(blob) > 6144 || string(blob[:prefix]) != kmsBlobPrefix {
		return "", "", nil, invalid
	}
	n := int(blob[prefix])
	start := prefix + 1
	if n == 0 || len(blob) < start+n+36+29 {
		return "", "", nil, invalid
	}
	region = string(blob[start : start+n])
	id = string(blob[start+n : start+n+36])
	if !kmsRegion(region) || !kmsUUID.MatchString(id) {
		return "", "", nil, invalid
	}
	return region, id, blob[start+n+36:], nil
}
func kmsOpen(material []byte, region, id string, context map[string]string, encrypted []byte) ([]byte, error) {
	if len(material) != 32 {
		return nil, kmsInternal()
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, kmsInternal()
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, kmsInternal()
	}
	aad, err := kmsAAD(region, id, context)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, encrypted, aad)
	if err != nil || len(plaintext) < 1 || len(plaintext) > 4096 {
		return nil, kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request.")
	}
	return plaintext, nil
}

// No labels, credentials or plaintext are placed in errors.
func kmsAlgorithm(value string) bool { return value == "" || value == "SYMMETRIC_DEFAULT" }
