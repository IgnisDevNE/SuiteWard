package installer

import (
	"archive/tar"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The load is one stream with its own transaction around it: BEGIN, the empty schema (the application keeps every table in the public
// schema, so emptying it empties the database, docs/backup.md section 3), the dump, and COMMIT as the very last statement. It is not
// psql's --single-transaction, which commits whatever the stream turns out to be: a stream that is cut short (Ctrl-C ends the command,
// the input ends, a COPY is truncated) must end inside a transaction that was never committed, which the server rolls back.
const (
	loadBegin  = "BEGIN;\nDROP SCHEMA public CASCADE;\nCREATE SCHEMA public;\n"
	loadCommit = "\nCOMMIT;\n" // after a newline: a dump may end without one
)

// The stage a restore is in, which decides what its error says about the data.
type restoreStage int

const (
	stageStop restoreStage = iota + 1
	stageLoad
	stageArtifacts
	stageStart
	stageCheck
)

// Restore is `suiteward restore <backup.tar.age> [--kit PATH] [flags]`, the guided restore of ADR 0020 into the instance that is
// installed here: verify the whole backup before anything changes, show what it holds beside the instance that runs, confirm,
// back up what is replaced, stop the service, load the dump, replace the artifacts, migrate and start, and compare what the service
// reports with the status of the backup. The credentials of the installation are never restored: a backup holds none.
func Restore(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	set := newFlagSet("restore")
	instanceFlags(set)
	set.String("kit", "", "the recovery kit file (default: the private key line of the kit is pasted, hidden, or piped in; a key is never an argument)")
	set.String("plaintext-dir", "", "restore a backup directory of an earlier release (suiteward.sql, suiteward-artifacts.tar, status.json) instead of a file")
	set.Bool("yes", false, "without a terminal: confirm the restore, together with --instance (a terminal asks you to type the instance id)")
	set.String("instance", "", "without a terminal: the instance id of the backup, which --yes confirms")
	set.String("backup-dir", "", "where the backup of the state that is replaced goes (default: a backups directory beside the MCP token copy, or in ~/.config/suiteward of the service account on the isolated tier)")
	file, flags, err := parseWithFile(set, args, env.Term.Out)
	if err != nil {
		return err
	}
	if (file == "") == (flags["plaintext-dir"] == "") {
		return fmt.Errorf("%w: name the backup file, or --plaintext-dir for the directory of an earlier release, and not both", ErrUsage)
	}
	plain, err := wantsPlain(flags, nil)
	if err != nil {
		return err
	}
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		return err
	}
	defer endConsole(scr, &err)
	scr.Steps("Verify", "Preview", "Safety backup", "Stop", "Load", "Start", "Compare")
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	inst, err := locateInstance(ctx, r, env, flags)
	if err != nil {
		return err
	}
	if !inst.haveSettings && inst.s.Tier != config.TierIsolated { // a restore replaces the data: not of an instance that defaults picked
		if err := engineNamed(flags, inst.settingsPath); err != nil {
			return err
		}
	}
	x := &restoreRun{r: r, env: env, scr: scr, inst: inst, file: file, plaintext: flags["plaintext-dir"], kit: flags["kit"]}
	defer func() { err = errors.Join(err, x.cleanup()) }() // the unpacked backup holds the TOTP secret
	return x.run(ctx)
}

// restoreRun is one restore.
type restoreRun struct {
	r         *Resolver
	env       Env
	scr       *ui.Screen
	inst      instance
	file      string // the backup file, "" for a directory
	plaintext string // the directory of an earlier release, "" for a file
	kit       string // --kit

	identity *age.HybridIdentity // opens the backup; nil for a directory
	work     string              // a private directory the backup is unpacked in, which cleanup removes
	files    string              // the directory that holds the dump, the volume and the status of the backup
	info     archive.Info        // what the backup says about itself; for a directory what its status says
	doc      statusDoc           // the status the backup holds
	current  *statusDoc          // GET /status of the instance that runs; nil when it does not answer
	after    statusDoc           // GET /status of the restored instance

	safetyKey *age.HybridRecipient // the key the state that is replaced is backed up to
	safety    string               // that backup
	stage     restoreStage
}

func (x *restoreRun) run(ctx context.Context) error {
	if err := x.verify(ctx); err != nil {
		return err
	}
	x.scr.Step(2)
	if err := x.preview(); err != nil {
		return err
	}
	x.scr.Step(3)
	if err := x.takeSafetyBackup(ctx); err != nil {
		return err
	}
	for i, step := range []func(context.Context) error{x.stop, x.load, x.start, x.compare} {
		x.scr.Step(4 + i)
		if err := step(ctx); err != nil {
			return x.failed(err)
		}
	}
	x.finish()
	return showSummary(x.scr, x.summary)
}

func (x *restoreRun) cleanup() error {
	if x.work == "" {
		return nil
	}
	return os.RemoveAll(x.work)
}

// source is how the command names the backup again.
func (x *restoreRun) source() string {
	if x.plaintext != "" {
		return "--plaintext-dir " + quoteArg(x.plaintext)
	}
	return quoteArg(x.file)
}

// restored is how the summary names the backup: the path as it is, where source quotes it for a command line.
func (x *restoreRun) restored() string {
	if x.plaintext != "" {
		return "plaintext directory " + x.plaintext
	}
	return x.file
}

// namesRepositories says that the backup holds at least one repository, which is what a default branch on GitHub to be behind needs.
func (x *restoreRun) namesRepositories() bool { return len(x.doc.repositories()) > 0 }

// ---- verify: nothing is changed ----

func (x *restoreRun) verify(ctx context.Context) error {
	if err := x.preflight(ctx); err != nil {
		return err
	}
	// The directory of the backup of the state that is replaced is where a restore unpacks too, and where one that was killed left its own.
	if err := ensureParent(x.inst.parent); err != nil {
		return err
	}
	if err := removeLeftovers(x.inst.parent, x.scr.Warn); err != nil {
		return err
	}
	var err error
	if x.plaintext != "" {
		x.files = x.plaintext
	} else {
		err = x.unpack(ctx)
	}
	if err != nil {
		return err
	}
	if err := x.checkFiles(); err != nil {
		return err
	}
	return x.checkRelease(ctx)
}

// preflight refuses a stack that cannot take the restore, before the backup is even opened: the load stops at the first error, so
// the database must run and the application role must exist.
func (x *restoreRun) preflight(ctx context.Context) error {
	if err := x.inst.restore.unavailable; err != nil {
		return fmt.Errorf("this stack cannot be restored: %w", err)
	}
	state, err := x.inst.rt.State(ctx, ContainerDB)
	if err != nil {
		return fmt.Errorf("look at the database container: %w", err)
	}
	if state.Status != "running" || state.Health != "healthy" {
		return fmt.Errorf("the database container must run and be healthy for a restore (it is %q, %q): start it (%s) and run the restore again", state.Status, state.Health, x.inst.startCommand)
	}
	exists, err := appRoleExists(ctx, x.inst.rt)
	if err != nil {
		return fmt.Errorf("look for the role %s: %w", appRole, err)
	}
	if !exists {
		return fmt.Errorf("the application role %s does not exist in this database, and the dump records grants to it: install the release of the backup first, which creates it, then run the restore again", appRole)
	}
	return nil
}

// unpack opens the backup with the kit and verifies all of it, writing nothing; then it unpacks it into a private directory, which
// verifies it again, and requires the two readings to agree. Everything that can fail in the file fails here, before the instance is touched.
func (x *restoreRun) unpack(ctx context.Context) error {
	identity, err := x.readIdentity()
	if err != nil {
		return err
	}
	x.identity = identity
	verified, err := x.readArchive(func(r *archive.Reader) (archive.Manifest, error) { return r.ReadManifest() })
	if err != nil {
		return err
	}
	if err := validateInfo(verified.Info); err != nil { // before anything is unpacked: the manifest is text of whoever wrote the archive
		return err
	}
	if x.work, err = os.MkdirTemp(x.inst.parent, ".suiteward-restore-"); err != nil {
		return fmt.Errorf("create a private directory in %s: %w", x.inst.parent, err)
	}
	if err := restrictPath(ctx, x.env.Deps, x.work, true); err != nil {
		return err
	}
	unpacked, err := x.readArchive(func(r *archive.Reader) (archive.Manifest, error) { return r.Extract(x.work) })
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(verified, unpacked) {
		return fmt.Errorf("%w: the backup changed while it was read", archive.ErrInvalid)
	}
	x.info, x.files = verified.Info, x.work
	return nil
}

// validateInfo refuses a manifest whose fields are not what the installer writes: anyone who holds the public recipient can write an archive
// that decrypts, and a restore prints these fields and builds a command from one. The digest may be empty (a backup of a stack whose
// image was not known); the version and the tier are plain names; the instance is the 8-hex id of /status, or "unknown" for a backup
// that could not name it. The refusal names the field and never repeats the value, which may be anything.
func validateInfo(i archive.Info) error {
	var field string
	switch {
	case i.ImageDigest != "" && !imageDigest.MatchString(i.ImageDigest):
		field = "image digest"
	case i.Instance != unknownValue && !instanceID.MatchString(i.Instance):
		field = "instance"
	case !plainName.MatchString(i.SuitewardVersion):
		field = "version"
	case i.Tier != "" && !plainName.MatchString(i.Tier):
		field = "tier"
	default:
		return nil
	}
	return fmt.Errorf("%w: the %s in the manifest of the backup is not what it should be (the value is not shown)", archive.ErrInvalid, field)
}

// readArchive opens the file again, because a Reader reads its stream once.
func (x *restoreRun) readArchive(read func(r *archive.Reader) (archive.Manifest, error)) (archive.Manifest, error) {
	f, err := os.Open(filepath.Clean(x.file))
	if err != nil {
		return archive.Manifest{}, fmt.Errorf("open the backup %s: %w", x.file, err)
	}
	defer func() { _ = f.Close() }()
	r, err := archive.Open(f, x.identity)
	if err != nil {
		return archive.Manifest{}, fmt.Errorf("verify the backup %s: %w", x.file, err)
	}
	m, err := read(r)
	if err != nil {
		return archive.Manifest{}, fmt.Errorf("verify the backup %s: %w", x.file, err)
	}
	return m, nil
}

// maxKit bounds what is read of a recovery kit, which is a few lines.
const maxKit = 64 << 10

// readIdentity is the owner's private key, from the kit file or from a hidden paste (a pipe where there is no terminal). Never an argument.
func (x *restoreRun) readIdentity() (*age.HybridIdentity, error) {
	var kit []byte
	var err error
	if x.kit != "" {
		kit, err = readKit(x.kit)
	} else {
		x.r.explain([]string{"recovery-key"}, "Open the backup",
			ui.Para("A backup is encrypted to the recovery key of its instance. Paste the last line of the recovery kit that was saved when that key was made: it is the private key. "+
				"The input is hidden and nothing of it is kept. To read the kit from a file instead, run the restore again with --kit PATH."))
		kit, err = x.r.Secret("recovery-key", "Recovery key: the last line of your recovery kit, the private key")
	}
	if err != nil {
		return nil, err
	}
	return archive.ParseIdentity(string(kit))
}

func readKit(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read the recovery kit %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	kit, err := io.ReadAll(io.LimitReader(f, maxKit+1))
	if err != nil {
		return nil, fmt.Errorf("read the recovery kit %s: %w", path, err)
	}
	if len(kit) > maxKit {
		return nil, fmt.Errorf("%s is too large to be a recovery kit", path)
	}
	return kit, nil
}

// checkFiles runs the checks of docs/backup.md, section 3, on what the backup holds, and reads its status.
func (x *restoreRun) checkFiles() error {
	for _, name := range []string{backupDump, backupVolume, backupStatus} {
		if _, err := os.Stat(filepath.Join(x.files, name)); err != nil {
			return fmt.Errorf("the backup has no %s: %w", name, err)
		}
	}
	if err := dumpComplete(filepath.Join(x.files, backupDump)); err != nil {
		return err
	}
	if err := tarReadable(filepath.Join(x.files, backupVolume)); err != nil {
		return err
	}
	statusPath := filepath.Join(x.files, backupStatus)
	raw, err := os.ReadFile(filepath.Clean(statusPath))
	if err != nil {
		return fmt.Errorf("read the status of the backup: %w", err)
	}
	if x.doc, err = parseBackedUpStatus(raw); err != nil {
		return fmt.Errorf("the status of the backup: %w", err)
	}
	if x.doc.Unavailable && x.plaintext != "" { // only an archive of this release records that: a directory that says so is not a backup
		return errors.New("the status of this directory says it was unavailable, which only an encrypted backup of this release records: it is not a backup this command restores")
	}
	if x.doc.Unavailable { // taken while the service did not answer: what the manifest says is all there is
		x.doc.Version, x.doc.Instance = x.info.SuitewardVersion, x.info.Instance
	}
	if x.plaintext != "" { // nothing records when a directory was made but its files
		info, err := os.Stat(statusPath)
		if err != nil {
			return fmt.Errorf("look at the status of the backup: %w", err)
		}
		x.info = archive.Info{SuitewardVersion: x.doc.Version, Instance: x.doc.Instance, CreatedAt: info.ModTime()}
	}
	return nil
}

// checkRelease refuses a backup of another release than the one that runs: migrations only go forward, so the schema of the dump must
// be the one of the release that loads it. It asks /status, and without an answer compares the digest of the installed image.
func (x *restoreRun) checkRelease(ctx context.Context) error {
	if body, err := x.inst.status(ctx); err == nil {
		if doc, err := parseStatus(body); err == nil {
			x.current = &doc
		}
	}
	switch {
	case x.doc.Unavailable: // no version was recorded: the image is the release
		if x.info.ImageDigest == "" || digestOf(x.inst.image) != x.info.ImageDigest {
			return x.otherRelease("the release " + digestOf(x.inst.image))
		}
	case x.current != nil && x.current.Version != x.info.SuitewardVersion:
		return x.otherRelease(x.current.Version)
	case x.current == nil && x.info.ImageDigest == "":
		return fmt.Errorf("the service does not answer /status, and a backup without a manifest does not name its image, so its release (%s) cannot be compared with the one installed: start the service (%s) and run the restore again", x.info.SuitewardVersion, x.inst.startCommand)
	case x.current == nil && digestOf(x.inst.image) != x.info.ImageDigest:
		return x.otherRelease("the release " + digestOf(x.inst.image))
	}
	return nil
}

func (x *restoreRun) otherRelease(running string) error {
	install := ""
	if x.info.ImageDigest != "" {
		install = " (`" + x.env.Deps.command() + " install --image " + releaseImageRepo + "@" + x.info.ImageDigest + "`)"
	}
	madeBy := "SuiteWard " + x.info.SuitewardVersion
	if x.doc.Unavailable {
		madeBy = "the release of the image " + x.info.ImageDigest
	}
	return fmt.Errorf("this backup was made by %s and this instance runs %s: a restore needs the same release, because migrations only go forward. Install the release of the backup first%s, then run the restore again", madeBy, running, install)
}

// dumpTail is how much of the end of a dump is searched for the line that ends it.
const dumpTail = 4096

// dumpComplete is the check of the runbook: a dump that was cut short must not be restored.
func dumpComplete(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read the dump: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("read the dump: %w", err)
	}
	// shortcut: the line that ends a dump is looked for in its last 4 KiB, where pg_dump puts it (only a \unrestrict line may follow).
	n := min(info.Size(), dumpTail)
	tail := make([]byte, n)
	if _, err := f.ReadAt(tail, info.Size()-n); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the dump: %w", err)
	}
	if !strings.Contains(string(tail), "PostgreSQL database dump complete") {
		return errors.New("the dump of the backup was cut short: it does not end as pg_dump ends a dump, so it is never loaded")
	}
	return nil
}

// tarReadable is the check of the runbook for the artifacts: the tarball can be listed to its end.
func tarReadable(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read the artifacts: %w", err)
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || info.Size() == 0 {
		return errors.New("the artifacts of the backup are an empty file, not a tar file")
	}
	if err := listTar(f); err != nil {
		return fmt.Errorf("the artifacts of the backup are not a readable tar file: %w", err)
	}
	return nil
}

// ---- preview and confirmation ----

func (x *restoreRun) preview() error {
	var fingerprint string
	var err error
	if x.scr.FullScreen() { // the key of the safety backup is a fact of the page, and a refusal for want of one comes before it
		if fingerprint, err = x.chooseSafetyKey(); err != nil {
			return err
		}
		x.previewPage(fingerprint)
		x.warn()
	} else {
		x.showBackup()
		x.showInstance()
		x.warn()
		if fingerprint, err = x.chooseSafetyKey(); err != nil {
			return err
		}
		x.scr.Say("The state of this instance is backed up first, encrypted to the recovery key %s.\n", fingerprint)
	}
	if x.current == nil {
		x.scr.Warn("This instance does not answer /status, so that backup records its status as unavailable, with the reason.\n")
	}
	return x.confirm()
}

// previewOrder is the order of the facts on the page of the confirmation. A window that is too small trims the page from its end, so the
// facts that say what is being replaced by what come first.
var previewOrder = []string{"Instance", "Release", "Owner", "Repositories", "Tier", "Made", "Schema", "Safety backup", "Backup"}

// previewPage is the page of the confirmation on a full screen: what it is, what runs now, and what the backup holds. The warnings that
// follow it outlast it when the window is too small for all of it.
func (x *restoreRun) previewPage(safetyKey string) {
	source, facts := x.backupFacts(true)
	facts = append(facts, ui.KV{Key: "Backup", Value: source}, ui.KV{Key: "Safety backup", Value: "the state that is replaced, encrypted to " + safetyKey})
	slices.SortStableFunc(facts, func(a, b ui.KV) int { return slices.Index(previewOrder, a.Key) - slices.Index(previewOrder, b.Key) })
	instance := "This instance does not answer /status."
	if now := x.instanceFacts(); now != nil {
		parts := make([]string, len(now))
		for i, kv := range now {
			parts[i] = strings.ToLower(kv.Key) + " " + kv.Value
		}
		instance = "This instance runs: " + strings.Join(parts, ", ") + "."
	}
	x.scr.Page("Restore preview",
		ui.Para("This is a confirmation: nothing is replaced before it."),
		ui.Para(instance+" The backup holds:"),
		ui.Fields(facts...))
}

// backupFacts is where the backup is and what it says of itself. A page, which has few rows for it, names the file and shortens the digest.
func (x *restoreRun) backupFacts(compact bool) (source string, facts []ui.KV) {
	source = x.file
	if x.plaintext != "" {
		source = x.plaintext
	}
	release, digest := x.info.SuitewardVersion, x.info.ImageDigest
	if compact {
		source, digest = filepath.Base(source), digest[:min(len(digest), len("sha256:")+12)]
	}
	if digest != "" {
		release += " (" + digest + ")"
	}
	owner := cmp.Or(x.doc.owner(), "none enrolled")
	repos := cmp.Or(strings.Join(x.doc.repositories(), ", "), "none")
	schema := strconv.Itoa(x.doc.SchemaVersion)
	if x.doc.Unavailable {
		owner, repos, schema = "not recorded", "not recorded", "not recorded"
	}
	return source, []ui.KV{
		{Key: "Made", Value: x.info.CreatedAt.UTC().Format(time.RFC3339)},
		{Key: "Release", Value: release},
		{Key: "Instance", Value: x.doc.Instance},
		{Key: "Tier", Value: cmp.Or(x.info.Tier, "not recorded")},
		{Key: "Owner", Value: owner},
		{Key: "Repositories", Value: repos},
		{Key: "Schema", Value: schema},
	}
}

// instanceFacts is what the instance that is replaced runs, nil when its /status does not answer.
func (x *restoreRun) instanceFacts() []ui.KV {
	if x.current == nil {
		return nil
	}
	return []ui.KV{
		{Key: "Release", Value: x.current.Version},
		{Key: "Instance", Value: x.current.Instance},
		{Key: "Owner", Value: cmp.Or(x.current.owner(), "none enrolled")},
		{Key: "Schema", Value: strconv.Itoa(x.current.SchemaVersion)},
	}
}

func (x *restoreRun) listFacts(facts []ui.KV) {
	for _, kv := range facts {
		x.scr.Say("  %-13s %s\n", kv.Key+":", kv.Value)
	}
}

func (x *restoreRun) showBackup() {
	source, facts := x.backupFacts(false)
	x.scr.Say("The backup %s holds:\n", source)
	x.listFacts(facts)
}

func (x *restoreRun) showInstance() {
	now := x.instanceFacts()
	if now == nil {
		x.scr.Say("This instance does not answer /status.\n")
		return
	}
	x.scr.Say("This instance runs:\n")
	x.listFacts(now)
}

func (x *restoreRun) warn() {
	x.scr.Warn("Restoring replaces the database and the artifacts of this instance with the backup's. What was approved, proposed or promoted after the backup is lost, and the default branch on GitHub may be ahead of the restored canonical.\n")
	x.scr.Warn("Stop any other running instance that uses the same GitHub App before you go on: two instances would publish and promote for the same repositories.\n")
	if x.plaintext != "" {
		x.scr.Warn("This backup is a plaintext directory: it is not encrypted, and nothing verifies who made it or that it is untouched. Whoever could read %s could read the TOTP secret in it.\n", x.plaintext)
	}
	if x.doc.Unavailable {
		x.scr.Warn("The status of this backup is not recorded (%s): it is the state of an instance that did not answer when the backup was taken, so its release is known by its image alone and nothing but that the service answers can be compared after the restore.\n", x.doc.Reason)
	}
	if x.current != nil && x.doc.Instance != unknownValue && x.current.Instance != x.doc.Instance { // "unknown" names no instance, so it is no other one
		x.scr.Warn("This backup is of another instance (%s) than the one that runs here (%s): after the restore this instance is the backup's, and the authenticator entry of the backup is the one that works.\n", x.doc.Instance, x.current.Instance)
	}
	if tier := string(x.inst.s.Tier); x.info.Tier != "" && x.info.Tier != tier {
		if x.scr.FullScreen() { // one row: the page has few of them for the facts above the warnings, and the plain lines keep the reason
			x.scr.Warn("Backup tier: %s; this instance: %s.\n", x.info.Tier, tier)
		} else {
			x.scr.Warn("The backup was made on the %s tier and this instance is on the %s tier: the owner's enrollment records its tier, and /status says so after the restore.\n", x.info.Tier, tier)
		}
	}
}

// chooseSafetyKey decides how the state that is replaced is backed up, before the confirmation: a refusal then changes nothing. The state
// is always backed up, whether or not the service answers, because the database is healthy (the restore checks) and its dump and the
// volume can be taken. The key is the settings' own, else the one of the kit that opens this backup. It returns the fingerprint of the key.
func (x *restoreRun) chooseSafetyKey() (string, error) {
	var recipient string
	switch {
	case x.inst.s.BackupRecipient != "":
		recipient = x.inst.s.BackupRecipient
	case x.identity != nil:
		recipient = x.identity.Recipient().String()
	default:
		return "", errors.New("the state of this instance is backed up, encrypted, before it is replaced, and this instance has no recovery key (a plaintext backup has none to borrow): run `" + x.env.Deps.command() + " backup init` first, then run the restore again")
	}
	key, err := archive.ParseRecipient(recipient)
	if err != nil {
		return "", err
	}
	x.safetyKey = key
	return archive.Fingerprint(recipient), nil
}

// confirm is the owner typing the instance id of the backup, or --yes with --instance where nobody can type.
func (x *restoreRun) confirm() error {
	id := x.doc.Instance
	return x.r.confirmInstance(instanceConfirm{
		id: id, action: "restore", ofWhat: "of the backup",
		prompt: fmt.Sprintf("Type the instance id %s to replace the database and the artifacts of this instance with this backup: ", id),
	})
}

// ---- the steps that change the instance ----

func (x *restoreRun) takeSafetyBackup(ctx context.Context) error {
	b := x.inst.backup(x.env.Deps, x.inst.parent, x.safetyKey)
	b.tolerateStatus = true // an instance that does not answer is backed up too, with its status recorded as unavailable
	var path string
	err := working(x.scr, "Backing up the state that is replaced", func() (err error) {
		path, err = takeBackup(ctx, b)
		return err
	})
	if err != nil {
		return err
	}
	x.safety = path
	x.scr.Keep("The state before the restore is in %s, encrypted to the recovery key %s.\n", path, archive.Fingerprint(x.safetyKey.String()))
	return nil
}

func (x *restoreRun) stop(ctx context.Context) error {
	x.stage = stageStop
	if err := working(x.scr, "Stopping the service", func() error { return x.inst.restore.stop(ctx) }); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}
	return nil
}

// load empties the schema and loads the dump in one transaction that the stream frames (see loadBegin), then replaces the artifacts.
func (x *restoreRun) load(ctx context.Context) error {
	x.stage = stageLoad
	dump, err := os.Open(filepath.Clean(filepath.Join(x.files, backupDump)))
	if err != nil {
		return fmt.Errorf("read the dump: %w", err)
	}
	defer func() { _ = dump.Close() }()
	sql := io.MultiReader(strings.NewReader(loadBegin), dump, strings.NewReader(loadCommit))
	if err := working(x.scr, "Loading the database", func() error { return loadDump(ctx, x.inst.rt, sql) }); err != nil {
		return err
	}
	x.stage = stageArtifacts
	return working(x.scr, "Replacing the artifacts", func() error {
		if err := x.inst.restore.clearVolume(ctx); err != nil {
			return err
		}
		return x.inst.rt.ImportVolume(ctx, VolumeData, filepath.Join(x.files, backupVolume))
	})
}

// loadDump runs psql on the stream of the load. ON_ERROR_STOP ends it at the first failed statement, inside the open transaction. Its
// message is never shown: psql repeats the rows it fails on, and those rows hold the TOTP secret.
func loadDump(ctx context.Context, rt Runtime, sql io.Reader) error {
	argv := []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-U", ownerRole, "-d", "suiteward", "-f", "-"}
	if _, err := rt.Exec(ctx, ContainerDB, argv, sql); err != nil {
		return fmt.Errorf("psql in %s could not load the dump (its message is not shown: psql repeats the rows it fails on, and they hold the TOTP secret; the log of the database container may quote rows too, the TOTP secret included, so read it as something secret)", ContainerDB)
	}
	return nil
}

func (x *restoreRun) start(ctx context.Context) error {
	x.stage = stageStart
	if err := x.inst.restore.start(ctx); err != nil {
		return err
	}
	after, err := x.waitForService(ctx)
	if err != nil {
		return err
	}
	x.after = after
	return nil
}

// waitForService waits for /status of the service that was started, as the install waits: a bounded number of tries.
func (x *restoreRun) waitForService(ctx context.Context) (statusDoc, error) {
	bar := barOn(x.scr, "Service")
	var last error
	for i := range statusTries {
		body, err := x.inst.status(ctx)
		if err == nil {
			var doc statusDoc
			if doc, err = parseStatus(body); err == nil {
				bar.set(1, "answers")
				return doc, nil
			}
		}
		if last = err; ctx.Err() != nil {
			break
		}
		bar.set(float64(i)/statusTries, "waiting for /status")
		if i < statusTries-1 {
			x.env.Sleep(statusEvery)
		}
	}
	return statusDoc{}, fmt.Errorf("the service was started, but /status does not answer (%w): read its logs", last)
}

// compare reports what the service says after the restore beside the status that was backed up. A different release, schema or owner
// is a restore that did not take; an integrity check that failed means the artifacts and the database do not come from one moment.
func (x *restoreRun) compare(context.Context) error {
	x.stage = stageCheck
	problems, notes := compareStatus(x.env.Deps.command(), x.doc, x.after)
	for _, note := range notes {
		x.scr.Warn("%s\n", note)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the restored instance does not match the backup: %s", strings.Join(problems, "; "))
	}
	if x.doc.Unavailable {
		x.scr.Check("The service answers /status as release %s. The backup records no status, so nothing else could be compared.\n", x.after.Version)
		return nil
	}
	x.scr.Check("The service reports the release %s, the schema %d and the owner %s of the backup.\n", x.after.Version, x.after.SchemaVersion, cmp.Or(x.after.owner(), "none"))
	return nil
}

func compareStatus(cmd string, backup, after statusDoc) (problems, notes []string) {
	if !backup.Unavailable { // a backup that records no status has nothing to be compared with but the integrity
		if after.Version != backup.Version {
			problems = append(problems, fmt.Sprintf("the service runs %s, the backup is of %s", after.Version, backup.Version))
		}
		if after.SchemaVersion != backup.SchemaVersion {
			problems = append(problems, fmt.Sprintf("the schema is %d, the backup's is %d", after.SchemaVersion, backup.SchemaVersion))
		}
		if after.owner() != backup.owner() {
			problems = append(problems, fmt.Sprintf("the owner is %q, the backup's is %q", after.owner(), backup.owner()))
		}
	}
	for _, g := range after.GitHub {
		switch g.Integrity {
		case "passed":
		case "failed":
			problems = append(problems, g.Repo+": the integrity check failed, so the artifacts and the database do not come from the same moment")
		default:
			notes = append(notes, g.Repo+": the integrity check has not passed yet (it runs when the service starts, after each promotion and every hour): look at `"+cmd+" doctor` in a moment")
		}
	}
	return problems, notes
}

// failed adds to the error of a step after the stop what the instance is in, and the ways on.
func (x *restoreRun) failed(err error) error {
	state := map[restoreStage]string{
		stageStop:      "the service may be stopped, and nothing else was changed",
		stageLoad:      "the database was not changed (the load is one transaction that is committed only after the last line of the dump, and the server rolls it back when a statement fails or the stream is cut short), and neither were the artifacts",
		stageArtifacts: "the database was replaced with the backup's, and the artifacts were not, or only in part",
		stageStart:     "the database and the artifacts are the backup's, but the service did not come up",
		stageCheck:     "the database and the artifacts are the backup's, and the service runs, but it does not match",
	}[x.stage]
	advice := fmt.Sprintf("The restore stopped here: %s.\nTo get the service back: %s.\nTo run the restore again: %s restore %s.", state, x.inst.restore.recovery, x.env.Deps.command(), x.source())
	if x.safety != "" {
		advice += fmt.Sprintf("\nTo go back to the state before the restore: %s restore %s, with the recovery kit of that backup.", x.env.Deps.command(), quoteArg(x.safety))
	}
	return fmt.Errorf("%w\n%s", err, advice)
}

// listTar reads a tar stream to its end.
func listTar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		if _, err := tr.Next(); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// canonicalBehind is what a restore leaves behind: the canonical it restored can be behind the default branch on GitHub.
const canonicalBehind = "The restored canonical may be behind the default branch on GitHub: to bring it level, open one corrective pull request from the current default branch (any small change outside the protected files), approve the revision its check names, and merge it (details: docs/backup.md)."

func (x *restoreRun) finish() {
	x.scr.Say("Restored the backup of the instance %s (%s) into this instance.\n", x.doc.Instance, x.info.SuitewardVersion)
	x.scr.Keep("The credentials of this installation are not in a backup, so they are as they were: the database passwords and the MCP token. The owner's TOTP enrollment is the backup's.\n")
	if !x.scr.FullScreen() && x.namesRepositories() { // a full screen has it on the summary page, which Close keeps
		x.scr.Keep("%s\n", canonicalBehind)
	}
	x.scr.Say("Check the instance with `%s doctor`.\n", x.env.Deps.command())
	if x.inst.s.BackupRecipient == "" {
		x.scr.Warn("No recovery key is set on this instance: run `%s backup init` to back it up.\n", x.env.Deps.command())
	}
}

func (x *restoreRun) summary() []ui.KV {
	rows := summaryRows{
		{Key: "Restored", Value: x.restored()},
		{Key: "Instance", Value: x.doc.Instance},
		{Key: "Release", Value: x.after.Version},
	}
	rows.add("Backup before", x.safety)
	if x.namesRepositories() {
		rows.warn(canonicalBehind)
	}
	return rows
}
