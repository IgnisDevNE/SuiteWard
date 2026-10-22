package installer

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// accountPersonal is the answer for an App owned by the user's personal account.
const accountPersonal = "personal"

// listenLoopback listens on 127.0.0.1 only; port 0 picks a free port.
func listenLoopback(port int) (net.Listener, error) { return nil, errors.New("not implemented") }

// convertedApp is what the manifest conversion returns, minus the secrets the installer has no use for.
type convertedApp struct {
	ID         int64
	Slug       string
	OwnerLogin string
	OwnerType  string
	PrivateKey []byte
}

// convertManifest exchanges the manifest code for the App (POST /app-manifests/{code}/conversions).
func convertManifest(ctx context.Context, client *http.Client, apiURL, code, account string) (convertedApp, error) {
	return convertedApp{}, errors.New("not implemented")
}

// obtainManifestCode serves the manifest form on a loopback listener and returns the code, from the callback or pasted.
func obtainManifestCode(ctx context.Context, env githubEnv, account string) (string, error) {
	return "", errors.New("not implemented")
}

// parseManifestCode reads a pasted code, or a pasted callback URL, whose state must be the expected one.
func parseManifestCode(input, state string) (string, error) { return "", errors.New("not implemented") }
