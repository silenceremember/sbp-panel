package buildinfo

const (
	Name       = "Simple Bridge Panel"
	Version    = "1.6.3"
	Prerelease = true
	Repository = "silenceremember/sbp-panel"
)

func RepositoryURL() string { return "https://github.com/" + Repository }
