package installer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The executor's bearer token is the service's secret like the MCP token, and nothing else of the stack reads it: the executor is
// another process that holds only this token (ADR 0028, section 10).

const (
	executorSecretUnitLine = "Secret=suiteward-executor-token,type=mount,target=/run/secrets/executor-token,uid=65532,mode=0400"
	executorEnvUnitLine    = "Environment=SUITEWARD_EXECUTOR_TOKEN_FILE=/run/secrets/executor-token"
)

var executorUnitLines = []string{executorSecretUnitLine, executorEnvUnitLine}

func TestTheServiceUnitMountsTheExecutorTokenAndNoOtherUnitDoes(t *testing.T) {
	serve := embeddedLines(t, "suiteward.container")
	for _, line := range executorUnitLines {
		if !slices.Contains(serve, line) {
			t.Errorf("suiteward.container lacks %s", line)
		}
	}
	for _, name := range []string{"suiteward-migrate.container", "suiteward-db.container"} {
		for _, line := range embeddedLines(t, name) {
			if strings.Contains(line, "executor") {
				t.Errorf("%s mentions the executor: %s", name, line)
			}
		}
	}
}

func TestRenderUnitsWithoutTheExecutorLeavesOutBothLines(t *testing.T) {
	units := renderedUnits(t, func(p *UnitParams) { p.NoExecutor = true })
	for _, line := range configLines(units["suiteward.container"]) {
		if strings.Contains(line, "executor") {
			t.Errorf("the service unit of a stack without an executor token has %s", line)
		}
	}
	for _, line := range executorUnitLines {
		if !slices.Contains(configLines(renderedUnits(t, nil)["suiteward.container"]), line) {
			t.Errorf("the default render lacks %s", line)
		}
	}
}

func TestAdoptUnitsAcceptsAServiceUnitThatPredatesTheExecutor(t *testing.T) {
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = withoutLines(t, installed["suiteward.container"], executorUnitLines...)
	if _, err := AdoptUnits(installed, nil); err != nil {
		t.Fatalf("a service unit of an earlier release is a known shape: %v", err)
	}
	if _, err := AdoptUnits(renderedUnits(t, nil), nil); err != nil {
		t.Fatalf("units with the lines are still known: %v", err)
	}
}

func TestAdoptUnitsStopsOnAServiceUnitWithOnlyOneOfTheExecutorLines(t *testing.T) {
	for _, line := range executorUnitLines {
		installed := renderedUnits(t, nil)
		installed["suiteward.container"] = withoutLines(t, installed["suiteward.container"], line)
		if diff := asDiffError(t, errAfter(AdoptUnits(installed, nil))); !diffMentions(diff, "executor-token") {
			t.Errorf("the diff must name the executor line that is missing: %+v", diff.Diffs)
		}
	}
}

func TestTheColocatedServiceMountsTheExecutorTokenAndNoOtherContainerDoes(t *testing.T) {
	stack := colocatedStack(t)
	if !slices.Contains(stack.Serve.Secrets, SecretExecutorAuth) || !slices.Contains(stack.Serve.Env, "SUITEWARD_EXECUTOR_TOKEN_FILE=/run/secrets/executor-token") {
		t.Errorf("serve: secrets %v, env %v", stack.Serve.Secrets, stack.Serve.Env)
	}
	for _, spec := range []RunSpec{stack.DB, stack.Migrate} {
		if slices.Contains(spec.Secrets, SecretExecutorAuth) || strings.Contains(strings.Join(spec.Env, " "), "EXECUTOR") {
			t.Errorf("%s reaches the executor token: %v %v", spec.Name, spec.Secrets, spec.Env)
		}
	}
}

// withoutExecutorToken removes the executor token from an inspected service container, as a container of an earlier release has it.
func withoutExecutorToken(obj map[string]any) {
	config := obj["Config"].(map[string]any)
	var env []string
	for _, kv := range config["Env"].([]string) {
		if !strings.HasPrefix(kv, "SUITEWARD_EXECUTOR_TOKEN_FILE=") {
			env = append(env, kv)
		}
	}
	config["Env"] = env
	var secrets []map[string]any
	for _, s := range config["Secrets"].([]map[string]any) {
		if s["Name"] != SecretExecutorAuth {
			secrets = append(secrets, s)
		}
	}
	config["Secrets"] = secrets
}

func TestAdoptContainersAcceptsAServiceThatPredatesTheExecutor(t *testing.T) {
	inspected := inspectedStack(t, localPS1Settings(), testImage, func(name string, obj map[string]any) {
		if name == ContainerServe {
			withoutExecutorToken(obj)
		}
	})
	if _, err := AdoptContainers(inspected); err != nil {
		t.Fatalf("a service container of an earlier release is a known shape: %v", err)
	}
}

func TestAdoptContainersStopsOnAServiceWithOnlyOneOfTheExecutorTokenParts(t *testing.T) {
	onlySecret := func(obj map[string]any) {
		config := obj["Config"].(map[string]any)
		var env []string
		for _, kv := range config["Env"].([]string) {
			if !strings.HasPrefix(kv, "SUITEWARD_EXECUTOR_TOKEN_FILE=") {
				env = append(env, kv)
			}
		}
		config["Env"] = env
	}
	onlyEnv := func(obj map[string]any) {
		config := obj["Config"].(map[string]any)
		var secrets []map[string]any
		for _, s := range config["Secrets"].([]map[string]any) {
			if s["Name"] != SecretExecutorAuth {
				secrets = append(secrets, s)
			}
		}
		config["Secrets"] = secrets
	}
	for name, edit := range map[string]func(map[string]any){"the secret without the variable": onlySecret, "the variable without the secret": onlyEnv} {
		inspected := inspectedStack(t, localPS1Settings(), testImage, func(container string, obj map[string]any) {
			if container == ContainerServe {
				edit(obj)
			}
		})
		if diff := asDiffError(t, errAfter(AdoptContainers(inspected))); !diffMentions(diff, "executor") {
			t.Errorf("%s: the diff must name the executor token: %+v", name, diff.Diffs)
		}
	}
}

func TestTheExecutorTokenSecretIsTheServicesAlone(t *testing.T) {
	if _, uid, err := secretFor(SecretExecutorAuth, ContainerServe); err != nil || uid != uidService {
		t.Errorf("serve: uid %d, %v", uid, err)
	}
	for _, container := range []string{ContainerMigrate, ContainerDB} {
		if _, _, err := secretFor(SecretExecutorAuth, container); err == nil {
			t.Errorf("%s may read the executor token", container)
		}
	}
	ctx := context.Background()
	f := newFake("linux")
	secrets := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	if err := secrets.Write(ctx, SecretExecutorAuth, []byte(secretValue), true); err != nil {
		t.Fatal(err)
	}
	runs := writerRuns(f)
	if len(runs) != 1 || !strings.Contains(runs[0].line(), "suiteward-secrets-"+ContainerServe+":/s") || !strings.Contains(runs[0].line(), "chown 65532:65532 /s/executor-token") {
		t.Fatalf("the executor token must reach the service's volume only: %v", f.lines())
	}
	f.assertNoSecret(t, secretValue)
}
