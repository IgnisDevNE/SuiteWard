package installer

// ImageDigest is the signed index digest of the release image (sha256:<64 hex>), set by the release workflow with
// -ldflags "-X github.com/IgnisDevNE/SuiteWard/internal/installer.ImageDigest=...". A development build leaves it empty.
var ImageDigest string

// ReleaseImage is the release image this binary installs by default, or false for a development build or an unusable digest.
func ReleaseImage() (string, bool) {
	if !imageDigest.MatchString(ImageDigest) {
		return "", false
	}
	return releaseImageRepo + "@" + ImageDigest, true
}
