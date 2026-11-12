//go:build !linux

package probeclock

func BootstrapInherited() (*Clock, error) { return nil, ErrClock }
func CloseInheritedExecutable() error     { return ErrClock }
