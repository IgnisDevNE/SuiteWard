package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The service account's half of the executor on the isolated tier. The host step (`sudo suiteward install host`) makes the executor, its
// token and a copy of that token that this account owns; this account turns the copy into the secret that the service reads.

// minExecutorTokenBytes is the shortest token the service accepts (runtime contract, EXECUTOR_TOKEN).
const minExecutorTokenBytes = 32

// executorHandoverFile is where the host step leaves the copy of the executor's token, in the account's configuration directory.
func executorHandoverFile(configDir string) string {
	return filepath.Join(configDir, executorTokenHandover)
}

// readExecutorHandover reads the copy of the executor's token that the host step left. found is false when there is none, which is the state of
// a host that was prepared by an earlier release. A copy that is too short is refused, naming the file and never the content.
func readExecutorHandover(configDir string) (token []byte, found bool, err error) {
	file := executorHandoverFile(configDir)
	raw, err := os.ReadFile(filepath.Clean(file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", file, err)
	}
	token = bytes.TrimSpace(raw)
	if len(token) < minExecutorTokenBytes {
		return nil, false, fmt.Errorf("%s holds a token shorter than %d bytes, which the service refuses. An administrator removes %s and runs `sudo suiteward install host --sha256sums <the SHA256SUMS of the release>` again, which makes a new one", file, minExecutorTokenBytes, executorAuthEtc)
	}
	return token, true, nil
}

// settleExecutorSecret makes the service's executor token the host step's: made when it is missing, replaced when it is another. A token that is
// not the host step's would lock the executor out, and the service reads its secrets only when it starts, so written says that a running
// service has to be restarted. Without a copy of the host step's there is nothing to make: exists is whether the secret is there all the same,
// and the service unit mounts it only then.
func settleExecutorSecret(ctx context.Context, store SecretStore, token []byte, found bool) (exists, written bool, err error) {
	have, err := store.Exists(ctx, SecretExecutorAuth)
	if err != nil {
		return false, false, err
	}
	if !found {
		return have, false, nil
	}
	if have {
		stored, err := store.Read(ctx, SecretExecutorAuth)
		if err != nil {
			return false, false, err
		}
		if bytes.Equal(bytes.TrimSpace(stored), token) {
			return true, false, nil
		}
	}
	if err := store.Write(ctx, SecretExecutorAuth, token, have); err != nil {
		return false, false, err
	}
	return true, true, nil
}

// noExecutorNote is what an install or an upgrade says when the service runs without the executor's token. again is the command to run after the host
// step: an upgrade cannot run again at the release that is installed, so it names the install.
func noExecutorNote(again string) string {
	return "The host has no token for the test runner (the executor) for this account yet, so the service runs without one and cannot run the approved tests. " +
		"An administrator makes the test runner and its token with `sudo suiteward install host --sha256sums <the SHA256SUMS of the release>`; then run " + again + ".\n"
}

// executorHostNote is what an uninstall says of the executor: it is the administrator's, and the service account cannot reach it.
func executorHostNote() string {
	return fmt.Sprintf("The test runner (the executor), if the host step made one, belongs to the administrator: its account %[1]s, its unit, its binary and its token were not touched. "+
		"It keeps asking for jobs from a service that is gone. To stop it, the administrator runs: systemctl --user --machine=%[1]s@.host disable --now %[2]s "+
		"(to remove all of it: also delete %[3]s, %[4]s, %[5]s and %[6]s, the nft unit %[7]s and the account).\n",
		ExecutorAccount, executorUnitName, executorUnitPath, executorLibDir, executorAuthEtc, executorRuleFile, executorBlockUnit)
}

// executorSetup is what the host step left for this account: the copy of the executor's token, when there is one.
type executorSetup struct {
	token []byte
	found bool
}

// readExecutorSetup reads what the host step left, before anything is changed.
func readExecutorSetup(configDir string) (executorSetup, error) {
	token, found, err := readExecutorHandover(configDir)
	return executorSetup{token: token, found: found}, err
}

// settle makes the service's executor token the host step's, and has the service unit mount it only when it exists (params.NoExecutor), saying so
// when it does not. again names what to run after the host step. restart is whether a running service has to be restarted to read what changed:
// it is the restart that was asked for already (secretChanged), or a token that was written now.
func (e executorSetup) settle(ctx context.Context, store SecretStore, scr *ui.Screen, params *UnitParams, again string, secretChanged bool) (restart bool, err error) {
	have, written, err := settleExecutorSecret(ctx, store, e.token, e.found)
	if err != nil {
		return false, err
	}
	params.NoExecutor = !have
	if !have {
		scr.Warn("%s", noExecutorNote(again))
	}
	return secretChanged || written, nil
}

// currentNote is what an upgrade of an instance that is current already says of the executor: the host step made a token that a service whose
// unit has no executor lines does not have yet. It is empty when there is nothing to say.
func (e executorSetup) currentNote(serviceWithout bool, install string) string {
	if !serviceWithout || !e.found {
		return ""
	}
	return fmt.Sprintf("The host step made a test runner token that the service does not have yet. Run %#q to give it, which restarts the service.\n", install)
}

// rotated is whether the service has an executor token secret that is not the host step's copy: the administrator removed the token and the host
// step made a new one, and the executor, which reads the new one, is locked out by the service's old one.
func (e executorSetup) rotated(ctx context.Context, store SecretStore) (bool, error) {
	if !e.found {
		return false, nil
	}
	have, err := store.Exists(ctx, SecretExecutorAuth)
	if err != nil || !have {
		return false, err
	}
	stored, err := store.Read(ctx, SecretExecutorAuth)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(bytes.TrimSpace(stored), e.token), nil
}

// settleCurrent is what an upgrade at the installed release does about a rotated token instead of saying that there is nothing to upgrade: the
// secret becomes the host step's, and the service restarts, as it reads its secrets only when it starts. settled is false when nothing was rotated.
func (e executorSetup) settleCurrent(ctx context.Context, env Env, scr *ui.Screen, host isolatedHost, httpPort int) (settled bool, err error) {
	rotated, err := e.rotated(ctx, host.rt.Secrets())
	if err != nil || !rotated {
		return false, err
	}
	if _, _, err := settleExecutorSecret(ctx, host.rt.Secrets(), e.token, e.found); err != nil {
		return false, err
	}
	if _, err := startStack(ctx, host.rt, env.Deps, scr, true); err != nil {
		return false, err
	}
	if err := waitStable(ctx, env, scr, httpPort); err != nil {
		return false, err
	}
	_, err = fmt.Fprintf(env.Term.Out, "The host step made a new test runner token. It replaced the service's, and the service was restarted to read it. The release is current already.\n")
	return true, err
}

// upgradeCurrent is an upgrade of an instance that is on the release already: it settles an executor token that the host step rotated, and
// otherwise says that there is nothing to upgrade, and what is left to do for the executor.
func upgradeCurrent(ctx context.Context, env Env, r *Resolver, scr *ui.Screen, host isolatedHost, e executorSetup, serviceWithout bool, image string, httpPort int) error {
	settled, err := e.settleCurrent(ctx, env, scr, host, httpPort)
	if err != nil {
		return err
	}
	if !settled {
		_, _ = fmt.Fprintf(env.Term.Out, "Nothing to upgrade: this instance is already on %s. To check that everything works, run `%s doctor`.\n", digestOf(image), env.Deps.command())
		_, _ = fmt.Fprint(env.Term.Out, e.currentNote(serviceWithout, installCommand(env.Deps.command(), r, true)))
	}
	return holdPage(scr)
}
