package main

import "runtime/debug"

const unknownVersion = "unknown"

var buildVersion string

func version() string {
	info, ok := debug.ReadBuildInfo()

	return resolveVersion(buildVersion, info, ok)
}

func resolveVersion(injected string, info *debug.BuildInfo, ok bool) string {
	if injected != "" {
		return injected
	}
	if !ok {
		return unknownVersion
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}

	return unknownVersion
}
