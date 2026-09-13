package main

const unknownVersion = "unknown"

var (
	releaseVersion string
	gitSHA         string
	buildVersion   string
)

func init() {
	buildVersion = selectVersion(releaseVersion, gitSHA)
}

func selectVersion(release, sha string) string {
	if release != "" {
		return release
	}
	if sha != "" {
		return sha
	}

	return unknownVersion
}
