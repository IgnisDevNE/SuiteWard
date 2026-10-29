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

// settingsFlag is the settings file the run names: --settings, else the installation that was chosen. The answers file never names it.
func (r *Resolver) settingsFlag() string { return cmp.Or(r.flags["settings"], r.chosen["settings"]) }

// namesInstallation says whether the run names an installation: with --settings, or with a token file (the flag or its answers-file key), which is
// the other file that tells two installations apart. The tier, the runtime and the connection do not: with several installations, a run that gives
// only one of them has not said which installation it is for.
func (r *Resolver) namesInstallation() bool {
	_, settings := r.flags["settings"]
	_, tokenFile := r.given("token-file")
	return settings || tokenFile
}

// chooseInstallation settles which installation a command works on when no flag names one: it asks when there are several and a terminal
// can be asked, uses the one there is and says which, and refuses where nobody can be asked: with no terminal, and under --yes or --answers, so
// that a script never waits on the question and never gets the default one silently. The choice goes into r.chosen, which instance(), preset
// and settingsFlag read after the flags and the answers file; given and asks do not, so a chosen installation is not a typed one. The default
// installation, chosen, sets nothing.
func (r *Resolver) chooseInstallation(d Deps) error {
	if r.namesInstallation() {
		return nil
	}
	found, stale, err := installationCandidates(d.GOOS)
	if err != nil {
		return fmt.Errorf("%w. To go on, say which installation with `--settings <file>` and `--token-file <file>`, or fix or delete that list. Each installation adds its own entry again the next time it is installed or upgraded", err)
	}
	switch {
	case len(found) == 0:
		return nil
	case len(found) == 1:
		r.say("This computer has one listed SuiteWard installation, so the command uses it. Its settings file is %s.\n", printable(found[0].Settings))
		r.choose(d, found[0])
		return nil
	case !r.term.IsTerminal || unattended(r.flags):
		files := make([]string, len(found))
		for i, c := range found {
			files[i] = printable(c.Settings)
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
// the flags that answer, and the way for a stack that has no settings file (a first install that failed before it saved them).
func severalInstallations(settingsFiles []string) string {
	return "This computer has several SuiteWard installations (" + strings.Join(settingsFiles, ", ") + "). Say which one with `--settings <file>` and `--token-file <file>`." +
		" A stack that has no settings file is named with `--token-file <file>` plus `--runtime` and `--connection`."
}

// pickInstallation asks which of the installations. No row is preselected and an empty answer picks nothing: Enter must never pick the main
// installation for a person who did not look. A full screen shows each installation in full above the choice, because a row of the choice cuts a long
// path and its description with it; a plain screen lists them as lines and asks for the typed settings file.
func (r *Resolver) pickInstallation(d Deps, found []candidate, stale int) (candidate, error) {
	intro := fmt.Sprintf("This computer has %d SuiteWard installations. Choose the one this command is for.", len(found))
	skipped := ""
	if stale == 1 {
		skipped = "1 listed installation was skipped: its settings file is gone."
	} else if stale > 1 {
		skipped = fmt.Sprintf("%d listed installations were skipped: their settings files are gone.", stale)
	}
	// The question has no key: a flag or an answers-file key of its name must not answer it, so it never goes through Value or Select of the resolver.
	var name string
	if r.screen.FullScreen() {
		blocks := []ui.Block{ui.Para(intro)}
		if skipped != "" {
			blocks = append(blocks, ui.Para(skipped))
		}
		choices := make([]ui.Choice, len(found))
		for i, c := range found {
			blocks = append(blocks, ui.Fields(ui.KV{Key: "Settings file", Value: c.Settings}, ui.KV{Key: "Details", Value: c.describe()}))
			choices[i] = ui.Choice{Name: c.Settings, Desc: c.describe()}
		}
		r.screen.Page("Which installation?", blocks...)
		picked, err := r.screen.Select("Installation", choices, -1)
		if err != nil {
			return candidate{}, err
		}
		name = choices[picked].Name
	} else {
		r.say("This computer has %d SuiteWard installations:\n", len(found))
		for _, c := range found {
			r.say("  %s\n    %s\n", printable(c.Settings), c.describe())
		}
		if skipped != "" {
			r.say("(%s)\n", skipped)
		}
		for name == "" {
			var err error
			if name, err = r.screen.Ask("Installation (type the settings file of one of them)", ""); err != nil {
				return candidate{}, err
			}
			if name == "" {
				r.say("Type the settings file of one of the installations listed above.\n")
			}
		}
	}
	i := slices.IndexFunc(found, func(c candidate) bool { return samePath(d.GOOS, c.Settings, name) })
	if i < 0 {
		return candidate{}, fmt.Errorf("%q is not the settings file of one of the installations listed above. Nothing was changed. Run the command again and type one of the settings files listed", name)
	}
	return found[i], nil
}

// choose makes c the installation of the run. Only what the default would not give is set: its settings file and its token file. The type and
// the engine are not: the settings file of a candidate exists and names them, and a list that went stale or was edited must not override it.
// An installation that is not the default one always keeps its token file, even the per-user default (a supported choice for a second instance):
// without it the run would refuse at instanceTokenFile for a flag that nobody typed.
func (r *Resolver) choose(d Deps, c candidate) {
	r.chosen = map[string]string{}
	if !c.isDefault {
		r.chosen["settings"] = c.Settings
	}
	if c.TokenFile != "" {
		if def, err := defaultTokenFile(d); !c.isDefault || err != nil || !samePath(d.GOOS, def, c.TokenFile) { // with no default to compare to, the entry's is the one
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
	instance, where, engine := "Instance not known yet", "on this computer", "with "+printable(c.Runtime)
	if c.Instance != "" {
		instance = "Instance " + printable(c.Instance)
	}
	if c.Tier == config.TierIsolated {
		where = "on a separate server"
	}
	if c.Connection != "" {
		engine += " (connection " + printable(c.Connection) + ")"
	}
	return prefix + strings.Join([]string{instance, where, engine, fmt.Sprintf("ports %d and %d", c.HTTPPort, c.MCPPort)}, ", ") + "."
}
