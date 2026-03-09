//go:build android

package avebi

type stopMode bool

const (
	stopModeManual     stopMode = true
	stopModeEndOfVideo stopMode = false
)
