package buildinfo

const (
	Name       = "Simple Bridge Panel"
	Version    = "1.7.2"
	Prerelease = true
	Repository = "silenceremember/sbp-panel"
)

func RepositoryURL() string { return "https://github.com/" + Repository }
