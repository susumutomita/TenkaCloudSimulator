//go:build !linux

package probeclock

func New() (*Clock, error) { return nil, ErrClock }
