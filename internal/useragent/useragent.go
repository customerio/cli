package useragent

const (
	productName    = "Customer.io-CLI"
	defaultVersion = "dev"
	repositoryURL  = "https://github.com/customerio/cli"
)

var version = defaultVersion

// SetVersion sets the CLI version included in outgoing User-Agent headers.
func SetVersion(v string) {
	version = v
}

// Get returns the User-Agent value used for outgoing CLI requests.
func Get() string {
	return productName + "/" + version + " (+" + repositoryURL + ")"
}
