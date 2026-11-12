package inference

// ErrAppleUnavailable is returned for every Apple Foundation Models request
// in this build. The native macOS adapter has not been verified on supported
// hardware, so selecting it fails instead of choosing another backend.
var ErrAppleUnavailable = &Error{Kind: Unavailable, Message: "the Apple Foundation Models backend is unavailable in this build: no verified macOS on-device adapter is included, and no other backend is selected"}

// AppleFoundationModels reports whether the Apple backend can be used. It
// currently always returns ErrAppleUnavailable on every platform.
func AppleFoundationModels() (Backend, error) { return nil, ErrAppleUnavailable }
