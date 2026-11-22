package db

import "syscall"

// tcpUserTimeout — TCP_USER_TIMEOUT (linux/tcp.h); в пакете syscall константы нет.
const tcpUserTimeout = 0x12

// setUserTimeout — Control net.Dialer: TCP_USER_TIMEOUT = UserTimeout для TCP (unix-сокет — без изменений).
func setUserTimeout(network, _ string, c syscall.RawConn) error {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil
	}
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(UserTimeout.Milliseconds())) // #nosec G115 -- дескриптор сокета
	}); err != nil {
		return err
	}
	return serr
}
