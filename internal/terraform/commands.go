package terraform

// Argument builders. They return argv slices (never shell strings) so no user
// value can be interpreted by a shell. dir is always passed via -chdir.

func chdir(bin, dir string) []string { return []string{bin, "-chdir=" + dir} }

func InitArgs(bin, dir string, upgrade bool) []string {
	a := append(chdir(bin, dir), "init", "-input=false", "-no-color")
	if upgrade {
		a = append(a, "-upgrade")
	}
	return a
}

// ForceUnlockArgs releases a state lock by ID (no prompt).
func ForceUnlockArgs(bin, dir, lockID string) []string {
	return append(chdir(bin, dir), "force-unlock", "-force", "--", lockID)
}

// WorkspaceArgs selects a workspace, creating it if missing.
func WorkspaceArgs(bin, dir, workspace string) []string {
	return append(chdir(bin, dir), "workspace", "select", "-or-create", workspace)
}

func ValidateArgs(bin, dir string) []string {
	return append(chdir(bin, dir), "validate", "-no-color")
}

// PlanArgs writes the plan to planFile. -detailed-exitcode: 0 no changes, 1 error, 2 changes.
func PlanArgs(bin, dir, planFile string, varFiles []string) []string {
	a := append(chdir(bin, dir), "plan", "-input=false", "-no-color", "-json", "-out="+planFile, "-detailed-exitcode", "-lock-timeout=0s")
	for _, v := range varFiles {
		a = append(a, "-var-file="+v)
	}
	return a
}

func ShowPlanArgs(bin, dir, planFile string) []string {
	return append(chdir(bin, dir), "show", "-json", "-no-color", planFile)
}

// ApplyArgs applies exactly the saved plan: nothing is re-planned.
func ApplyArgs(bin, dir, planFile string) []string {
	return append(chdir(bin, dir), "apply", "-input=false", "-no-color", "-json", "-lock-timeout=0s", planFile)
}

func StateSerialArgs(bin, dir string) []string {
	return append(chdir(bin, dir), "state", "pull")
}

// DriftArgs is a refresh-only plan: it compares state with real infrastructure
// and changes nothing. -detailed-exitcode: 0 no drift, 2 drift.
func DriftArgs(bin, dir, planFile string, varFiles []string) []string {
	a := append(chdir(bin, dir), "plan", "-refresh-only", "-input=false", "-no-color", "-json", "-out="+planFile, "-detailed-exitcode", "-lock-timeout=0s")
	for _, v := range varFiles {
		a = append(a, "-var-file="+v)
	}
	return a
}

func GraphArgs(bin, dir string) []string {
	return append(chdir(bin, dir), "graph", "-type=plan")
}
