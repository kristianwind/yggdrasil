package db

import "syscall"

func setUmask(m int) int { return syscall.Umask(m) }
