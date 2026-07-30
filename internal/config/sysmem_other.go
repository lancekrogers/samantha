//go:build !darwin

package config

func darwinMemTotalSysctl() uint64 { return 0 }
