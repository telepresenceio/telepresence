package setup

import "golang.org/x/sys/windows"

// errConnRefused is the socket error a host returns when nothing listens on the dialed port.
const errConnRefused = windows.WSAECONNREFUSED
