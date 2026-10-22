package installer

// PendingApp is the GitHub App an install created and has not finished with: the record that lets a re-run take it up instead
// of creating a second one. It never holds the key, which is in the secret store.
type PendingApp struct {
	AppID       int64  `yaml:"app_id"`
	Slug        string `yaml:"slug"`
	Account     string `yaml:"account"`      // the login that owns the App
	AccountType string `yaml:"account_type"` // User or Organization
}

// pendingAppPath is where an install records the GitHub App it created before it has finished with it, beside the settings file.
func pendingAppPath(settingsPath string) string { return settingsPath + ".pending-app" }
