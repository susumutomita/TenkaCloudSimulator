package simpledb

import (
	"regexp"
	"unicode/utf8"
)

var domainName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,255}$`)

func validText(s string, empty bool) bool {
	return (empty || len(s) > 0) && len(s) <= 1024 && utf8.ValidString(s)
}
func scope(a, r string) error {
	if !validText(a, false) || !validText(r, false) {
		return storeError("InvalidParameterValue")
	}
	return nil
}
func domainScope(a, r, d string) error {
	if err := scope(a, r); err != nil {
		return err
	}
	if !domainName.MatchString(d) {
		return storeError("InvalidParameterValue")
	}
	return nil
}
func itemScope(a, r, d, i string) error {
	if err := domainScope(a, r, d); err != nil {
		return err
	}
	if !validText(i, false) {
		return storeError("InvalidParameterValue")
	}
	return nil
}
