//go:build !windows

package winsvc

import "errors"

func dialSCM() (SCMConn, error) {
	return nil, errors.New("SCM not available on this platform")
}
