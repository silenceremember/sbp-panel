package buildinfo

const (
	Name       = "Simple Bridge Panel"
	Version    = "1.4.1"
	Prerelease = true
	Repository = "silenceremember/sbp-panel"
)

func RepositoryURL() string { return "https://github.com/" + Repository }
