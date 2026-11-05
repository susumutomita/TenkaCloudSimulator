package aws

import (
	"encoding/base64"
	"fmt"
)

// dynamoItemSize measures semantic attribute bytes, excluding JSON/base64 wire
// encoding and the separate 100-byte storage billing overhead. Revalidation is
// intentional: persisted documents are not trusted to be well-formed.
func dynamoItemSize(item map[string]any) (int64, error) {
	if len(item) == 0 {
		return 0, fmt.Errorf("Item must not be empty")
	}
	var size int64
	for name, value := range item {
		if err := validateDynamoValue(value, 0); err != nil {
			return 0, err
		}
		size += int64(len(name)) + dynamoValueSize(value)
	}
	return size, nil
}

// Only called on a validated AttributeValue with bounded depth and request size.
func dynamoValueSize(value any) int64 {
	for kind, raw := range value.(map[string]any) {
		switch kind {
		case "S":
			return int64(len(raw.(string)))
		case "B":
			decoded, _ := base64.StdEncoding.DecodeString(raw.(string))
			return int64(len(decoded))
		case "N":
			_, size, _ := parseDynamoDecimal(raw.(string))
			return size
		case "BOOL", "NULL":
			return 1
		case "SS", "NS", "BS":
			var size int64
			for _, el := range raw.([]any) {
				size += dynamoValueSize(map[string]any{kind[:1]: el})
			}
			return size
		case "L":
			size := int64(3)
			for _, el := range raw.([]any) {
				size += 1 + dynamoValueSize(el)
			}
			return size
		case "M":
			size := int64(3)
			for name, el := range raw.(map[string]any) {
				size += 1 + int64(len(name)) + dynamoValueSize(el)
			}
			return size
		}
	}
	panic("validated DynamoDB value has no type")
}
