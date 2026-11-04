package blob

import (
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// selectedRange implements Microsoft's documented two single-range formats.
// Suffix and multiple ranges are deliberately unsupported rather than inferred
// from generic HTTP behavior. It runs only after owner authorization and the
// same-transaction object/condition read, so 416 never exposes unauthorized size.
func selectedRange(header string, total int64) (int64, int64, error) {
	invalid := fail(400, "InvalidHeaderValue", "Only bytes=start-end or bytes=start- is supported.")
	if len(header) > 128 || !strings.HasPrefix(header, "bytes=") {
		return 0, 0, invalid
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" {
		return 0, 0, invalid
	}
	number := func(value string) (int64, error) {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, invalid
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, invalid
		}
		return n, nil
	}
	start, err := number(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end := total - 1
	if parts[1] != "" {
		end, err = number(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}
	if start >= total || end < start {
		return 0, 0, fail(416, "InvalidRange", "The specified range is not satisfiable.")
	}
	if end >= total {
		end = total - 1
	}
	return start, end, nil
}

// applyDownloadRange returns the selected response copy; persisted bytes and
// stored whole-blob MD5 are never changed. The caller publishes headers only
// after every range/checksum guard has succeeded.
func applyDownloadRange(r *http.Request, result entity) (entity, string, error) {
	rangeValues := r.Header.Values("Range")
	nativeValues := r.Header.Values("x-ms-range")
	hasRange := len(rangeValues) > 0 || len(nativeValues) > 0
	md5Values := r.Header.Values("x-ms-range-get-content-md5")
	wantMD5 := len(md5Values) > 0
	if wantMD5 && (len(md5Values) != 1 || md5Values[0] != "true" || !hasRange) {
		return entity{}, "", fail(400, "InvalidHeaderValue", "Range MD5 requires a single range and value true.")
	}
	if !hasRange {
		return result, "", nil
	}
	raw := r.Header.Get("Range")
	if len(nativeValues) > 0 {
		raw = r.Header.Get("x-ms-range")
	}
	start, end, err := selectedRange(raw, result.Length)
	if err != nil {
		return entity{}, "", err
	}
	size := end - start + 1
	if wantMD5 && size > 4<<20 {
		return entity{}, "", fail(400, "OutOfRangeInput", "Range MD5 is limited to four MiB.")
	}
	contentRange := fmt.Sprintf("bytes %d-%d/%d", start, end, result.Length)
	result.Body = result.Body[start : end+1]
	result.Length = size
	result.MD5 = ""
	if wantMD5 {
		sum := md5.Sum(result.Body)
		result.MD5 = base64.StdEncoding.EncodeToString(sum[:])
	}
	return result, contentRange, nil
}
