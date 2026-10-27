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
		return nil, false, fmt.Errorf("%s holds a token of fewer than %d bytes, which the service refuses: as the administrator, remove %s and run `sudo suiteward install host` again, which makes a new one", file, minExecutorTokenBytes, executorAuthEtc)
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
	return "The host has no executor token for this account yet, so the service runs without an executor and canonical execution has nothing to run on. " +
		"The administrator makes the executor and its token with `sudo suiteward install host`; then run " + again + ".\n"
}

// executorHostNote is what an uninstall says of the executor: it is the administrator's, and the service account cannot reach it.
func executorHostNote() string {
	return fmt.Sprintf("The executor, when the host step made one, is the administrator's: its account %[1]s, its unit, its binary and its token are not touched here. "+
		"It keeps asking for jobs of a service that is gone. As the administrator, to stop it: systemctl --user --machine=%[1]s@.host disable --now %[2]s "+
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
	return fmt.Sprintf("The host step made an executor token that the service does not have yet: run `%s` to give it, which restarts the service.\n", install)
}
