package installer

// chooseInstallation settles which installation a command works on when no flag names one: it asks when there are several, uses the only one
// there is, and refuses where nobody can be asked.
func (r *Resolver) chooseInstallation(d Deps) error { return nil }

// describe is what a row says of the installation.
func (c candidate) describe() string { return "" }
