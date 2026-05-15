package dns

import "errors"

var (
	ErrIPNotFound = errors.New("couldn't find ip")
	ErrIPVersion  = errors.New("ip version error")
)
