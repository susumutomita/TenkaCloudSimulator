package aws

import (
	"regexp"
	"strconv"
	"strings"
)

var dynamoDecimalSyntax = regexp.MustCompile(`^([+-]?)([0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE]([+-]?[0-9]+))?$`)

// parseDynamoDecimal uses decimal arithmetic, never binary floating point. The
// size is AWS's documented approximation (ceil(significant digits/2)+1), not
// a claim about its private number encoding, billing, or exact boundary parity.
func parseDynamoDecimal(s string) (canonical string, size int64, ok bool) {
	parts := dynamoDecimalSyntax.FindStringSubmatch(s)
	if parts == nil {
		return "", 0, false
	}
	exponent := int64(0)
	if parts[3] != "" {
		// Bound parsing before arithmetic/allocation; the entire request is <=1MiB,
		// so larger exponents cannot cancel a nonzero mantissa into DynamoDB range.
		negative := strings.HasPrefix(parts[3], "-")
		digits := strings.TrimLeft(strings.TrimLeft(parts[3], "+-"), "0")
		if len(digits) > 9 {
			return "", 0, false
		}
		if digits != "" {
			exponent, _ = strconv.ParseInt(digits, 10, 64)
		}
		if negative {
			exponent = -exponent
		}
	}
	mantissa := parts[2]
	fractional := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		fractional = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return "0", 2, true
	}
	significant := strings.TrimRight(digits, "0")
	if len(significant) > 38 {
		return "", 0, false
	}
	power := exponent - int64(fractional) + int64(len(digits)-len(significant))
	adjusted := power + int64(len(significant)) - 1
	if adjusted < -130 || adjusted > 125 {
		return "", 0, false
	}
	point := int64(len(significant)) + power
	switch {
	case point <= 0:
		canonical = "0." + strings.Repeat("0", int(-point)) + significant
	case point >= int64(len(significant)):
		canonical = significant + strings.Repeat("0", int(point)-len(significant))
	default:
		canonical = significant[:int(point)] + "." + significant[int(point):]
	}
	if parts[1] == "-" {
		canonical = "-" + canonical
	}
	return canonical, int64((len(significant)+1)/2 + 1), true
}
