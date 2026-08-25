package buildinfo

const (
	Name       = "Simple Bridge Panel"
	Version    = "1.3.0"
	Prerelease = false
	Repository = "silenceremember/sbp-panel"
)

func RepositoryURL() string { return "https://github.com/" + Repository }
