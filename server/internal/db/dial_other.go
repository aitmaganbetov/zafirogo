//go:build !linux

package db

import "syscall"

// setUserTimeout — TCP_USER_TIMEOUT есть только в Linux; на других системах — только keepalive.
func setUserTimeout(string, string, syscall.RawConn) error { return nil }
