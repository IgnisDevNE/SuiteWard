package prefetch

// PlanSide plans the proxy files for one side. goMod is the go.mod at the root of the composed tree and goSum its go.sum.
func PlanSide(goMod, goSum []byte, hasVendorModulesTxt bool, l Limits) (Plan, error) {
	return Plan{}, nil
}
