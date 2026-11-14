//go:build !linux && !darwin

package probeclock

func New() (*Clock, error) { return nil, ErrClock }
