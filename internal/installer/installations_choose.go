package installer

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// installationKeys are the flags that name an installation together: a person types up to five of them today.
var installationKeys = []string{"settings", "token-file", "tier", "runtime", "connection"}

// settingsFlag is the settings file the run names: --settings, else the installation that was chosen. The answers file never names it.
func (r *Resolver) settingsFlag() string { return cmp.Or(r.flags["settings"], r.chosen["settings"]) }

// namesInstallation says whether the run was given a flag, or an answers-file key but for settings, that names an installation.
func (r *Resolver) namesInstallation() bool {
	if _, ok := r.flags["settings"]; ok {
		return true
	}
	return slices.ContainsFunc(installationKeys[1:], func(key string) bool { _, ok := r.given(key); return ok })
}

// chooseInstallation settles which installation a command works on when no flag names one: it asks when there are several and a terminal
// can be asked, uses the one there is and says which, and refuses where nobody can be asked. The choice goes into r.chosen, which
// instance(), preset and settingsFlag read after the flags and the answers file; given and asks do not, so a chosen installation is not a
// typed one. The default installation, chosen, sets nothing.
func (r *Resolver) chooseInstallation(d Deps) error {
	if r.namesInstallation() {
		return nil
	}
	found, stale, err := installationCandidates(d.GOOS)
	if err != nil {
		return fmt.Errorf("%w. To go on, say which installation with `--settings <file>` and `--token-file <file>`, or delete that list: an install writes it again", err)
	}
	switch {
	case len(found) == 0:
		return nil
	case len(found) == 1:
		r.say("This computer has one SuiteWard installation, so the command uses it. Its settings file is %s.\n", found[0].Settings)
		r.choose(d, found[0])
		return nil
	case !r.term.IsTerminal:
		files := make([]string, len(found))
		for i, c := range found {
			files[i] = c.Settings
		}
		return errors.New(severalInstallations(files))
	}
	picked, err := r.pickInstallation(d, found, stale)
	if err != nil {
		return err
	}
	r.choose(d, picked)
	return nil
}

// severalInstallations is the refusal of a run that cannot be asked which of the installations it is for: it names the settings files and
// the flags that answer.
func severalInstallations(settingsFiles []string) string {
	return "This computer has several SuiteWard installations (" + strings.Join(settingsFiles, ", ") + "). Say which one with `--settings <file>` and `--token-file <file>`."
}

// pickInstallation asks which of the installations: a page and a list on a full screen, the list as lines and a typed settings file on a plain
// one.
func (r *Resolver) pickInstallation(d Deps, found []candidate, stale int) (candidate, error) {
	intro := fmt.Sprintf("This computer has %d SuiteWard installations. Choose the one this command is for.", len(found))
	skipped := ""
	if stale == 1 {
		skipped = "1 listed installation was skipped: its settings file is gone."
	} else if stale > 1 {
		skipped = fmt.Sprintf("%d listed installations were skipped: their settings files are gone.", stale)
	}
	blocks := []ui.Block{ui.Para(intro)}
	if skipped != "" {
		blocks = append(blocks, ui.Para(skipped))
	}
	choices := make([]ui.Choice, len(found))
	for i, c := range found {
		choices[i] = ui.Choice{Name: c.Settings, Desc: c.describe()}
	}
	// The question has no key: a flag or an answers-file key of its name must not answer it, so it never goes through Value or Select of the
	// resolver. The first row is the default installation when there is one.
	var name string
	if r.screen.FullScreen() {
		r.screen.Page("Which installation?", blocks...)
		picked, err := r.screen.Select("Installation", choices, 0)
		if err != nil {
			return candidate{}, err
		}
		name = choices[picked].Name
	} else {
		r.say("This computer has %d SuiteWard installations:\n", len(found))
		for _, c := range choices {
			r.say("  %s\n    %s\n", c.Name, c.Desc)
		}
		if skipped != "" {
			r.say("(%s)\n", skipped)
		}
		var err error
		if name, err = r.screen.Ask("Installation (type the settings file of one of them)", found[0].Settings); err != nil {
			return candidate{}, err
		}
	}
	i := slices.IndexFunc(found, func(c candidate) bool { return samePath(d.GOOS, c.Settings, name) })
	if i < 0 {
		return candidate{}, fmt.Errorf("%q is not the settings file of one of the installations listed above", name)
	}
	return found[i], nil
}

// choose makes c the installation of the run. Only what the default would not give is set: its settings file and its token file. The type and
// the engine are not: the settings file of a candidate exists and names them, and a list that went stale or was edited must not override it.
func (r *Resolver) choose(d Deps, c candidate) {
	r.chosen = map[string]string{}
	if !c.isDefault {
		r.chosen["settings"] = c.Settings
	}
	if c.TokenFile != "" {
		if def, err := defaultTokenFile(d); err != nil || !samePath(d.GOOS, def, c.TokenFile) { // with no default to compare to, the entry's is the one
			r.chosen["token-file"] = c.TokenFile
		}
	}
}

// describe is what a row says of the installation: which instance, where it runs, its engine and its two ports, and that it is the default.
func (c candidate) describe() string {
	prefix := ""
	if c.isDefault {
		prefix = "The default installation. "
	}
	if c.Runtime == "" {
		return prefix + "Its settings could not be read."
	}
	instance, where, engine := "Instance not known yet", "on this computer", "with "+c.Runtime
	if c.Instance != "" {
		instance = "Instance " + c.Instance
	}
	if c.Tier == config.TierIsolated {
		where = "on a separate server"
	}
	if c.Connection != "" {
		engine += " (connection " + c.Connection + ")"
	}
	return prefix + strings.Join([]string{instance, where, engine, fmt.Sprintf("ports %d and %d", c.HTTPPort, c.MCPPort)}, ", ") + "."
}
