package prefetch

import (
	"fmt"
	"go/version"
	"maps"
	"path"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const (
	// maxField bounds a go.sum field. No module path or version is longer, and a refusal never echoes more than this much candidate text.
	maxField = 1024
	// maxDetail bounds the candidate text that the message of a refusal carries.
	maxDetail = 300
)

// PlanSide plans the proxy files that one side needs. goMod is the go.mod at the root of the composed tree and goSum its go.sum; the tree is
// the directory that holds go.mod, so a nested module that replaces to a sibling directory is refused.
//
// A side whose go.mod names go 1.14 or later and that has vendor/modules.txt vendors, as the go command does, and needs nothing. Otherwise
// the plan is the .mod file of every go.sum line for a go.mod and the .zip file of every other line, minus the modules that go.mod replaces
// (the go command never asks the proxy for those, but it does for a module that a replace names as its target). A replace to a directory outside the tree, a go.mod or go.sum that does not validate and
// more than l.Modules distinct module versions are refused with ErrInstallFailed. l.Bytes is Fetch's.
func PlanSide(goMod, goSum []byte, hasVendorModulesTxt bool, l Limits) (Plan, error) {
	mod, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		return Plan{}, refuse("go.mod does not parse: %v", err)
	}
	// Go is absent when go.mod names no version, and then the go command does not use vendor/ either.
	if hasVendorModulesTxt && mod.Go != nil && version.Compare("go"+mod.Go.Version, "go1.14") >= 0 {
		return Plan{Vendor: true}, nil
	}
	replaced, err := replacedModules(mod.Replace)
	if err != nil {
		return Plan{}, err
	}
	modules := map[module.Version]struct{}{}
	names := map[string]struct{}{}
	for i, line := range strings.Split(string(goSum), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return Plan{}, refuse("go.sum line %d has %d fields, want 3", i+1, len(fields))
		}
		m, ext := module.Version{Path: fields[0], Version: strings.TrimSuffix(fields[1], "/go.mod")}, "zip"
		if m.Version != fields[1] {
			ext = "mod"
		}
		if replaced.away(m) {
			continue
		}
		name, err := proxyName(m, ext)
		if err != nil {
			return Plan{}, refuse("go.sum line %d: %v", i+1, err)
		}
		modules[m] = struct{}{}
		if len(modules) > l.Modules {
			return Plan{}, refuse("go.sum names more than %d modules", l.Modules)
		}
		names[name] = struct{}{}
	}
	files := make([]File, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		files = append(files, File{Name: name})
	}
	return Plan{Files: files}, nil
}

// replacements are the modules that go.mod replaces (an empty version is every version of the path) and the modules it replaces them with.
type replacements struct{ old, targets map[module.Version]struct{} }

// replacedModules reads the replace directives. It refuses a replace to a directory outside the tree: the directory of a replace has no
// version.
func replacedModules(replaces []*modfile.Replace) (replacements, error) {
	r := replacements{old: map[module.Version]struct{}{}, targets: map[module.Version]struct{}{}}
	for _, rep := range replaces {
		if rep.New.Version == "" && !insideTree(rep.New.Path) {
			return replacements{}, refuse("go.mod replaces %s with the directory %s, which is outside the tree", rep.Old.Path, rep.New.Path)
		}
		r.old[rep.Old] = struct{}{}
		r.targets[rep.New] = struct{}{}
	}
	return r, nil
}

// away reports whether the go command never asks the proxy for m: a replace covers it and no replace has it as its target (go uses the target
// of a replace as it is, and does not replace it again).
func (r replacements) away(m module.Version) bool {
	_, allVersions := r.old[module.Version{Path: m.Path}]
	_, thisVersion := r.old[m]
	_, target := r.targets[m]
	return (allVersions || thisVersion) && !target
}

// insideTree reports whether a replace directory stays inside the tree. The path is read the same way on every host: a backslash or a drive
// letter (a Windows path, which go refuses elsewhere) and a rooted path are outside.
func insideTree(dir string) bool {
	if strings.Contains(dir, `\`) || path.IsAbs(dir) || len(dir) >= 2 && dir[1] == ':' {
		return false
	}
	clean := path.Clean(dir)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// proxyName validates a go.sum module and names its file in the proxy protocol: <escaped path>/@v/<escaped version>.<ext>.
func proxyName(m module.Version, ext string) (string, error) {
	if len(m.Path) > maxField || len(m.Version) > maxField {
		return "", fmt.Errorf("a field is longer than %d bytes", maxField)
	}
	if err := module.Check(m.Path, m.Version); err != nil {
		return "", err
	}
	if module.CanonicalVersion(m.Version) != m.Version {
		return "", fmt.Errorf("version %q is not canonical", m.Version)
	}
	escPath, err := module.EscapePath(m.Path)
	if err != nil {
		return "", err
	}
	escVersion, err := module.EscapeVersion(m.Version)
	if err != nil {
		return "", err
	}
	return escPath + "/@v/" + escVersion + "." + ext, nil
}

// refuse is an ErrInstallFailed whose text carries a bounded amount of candidate text.
func refuse(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if len(detail) > maxDetail {
		detail = detail[:maxDetail] + "..."
	}
	return fmt.Errorf("%w: %s", ErrInstallFailed, detail)
}
