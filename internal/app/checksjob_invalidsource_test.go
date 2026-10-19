package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The comment of an invalid-source check (docs/contracts/runtime.md, "Outbox github_comment, key invalid-source:..."): the checks job
// explains the problem once, so the owner need not open the check. It is evidence: the check is the failure it was.

// markerKeyPattern is the pattern a marker key must match (commandtext.Marker).
var markerKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_-]{0,199}$`)

// linkedTree is a bootTree with symbolic links: links maps the path of each link to its target, which is the link's blob.
type linkedTree struct {
	*bootTree
	links map[string]string
}

func (t linkedTree) Entries(ctx context.Context) ([]inventory.Entry, error) {
	entries, err := t.bootTree.Entries(ctx)
	for _, path := range slices.Sorted(maps.Keys(t.links)) {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.Symlink})
	}
	return entries, err
}

func (t linkedTree) Read(ctx context.Context, path string) ([]byte, error) {
	if target, found := t.links[path]; found {
		return []byte(target), nil
	}
	return t.bootTree.Read(ctx, path)
}

// reachable is a declaration whose pattern can reach a directory behind a link, such as spec_link -> app.
const reachable = "version: 1\ninclude: ['test_*.py']\n"

// linkHead gives sha a tree with the reachable declaration, a directory app and one link to it for each of links.
func (w *cjJobWorld) linkHead(sha string, links ...string) {
	files := bootFiles("v1")
	files[".suiteward.yml"] = reachable
	files["app/main.py"] = "print('x')\n"
	targets := map[string]string{}
	for _, link := range links {
		targets[link] = "app"
	}
	w.reader.repos[bootRepo].trees[sha] = linkedTree{&bootTree{files: files}, targets}
}

// problemDigest is the digest of a problem: SHA-256 of its kind, a newline and the whole path, and for a link that carries the head's
// declaration, a newline and the hex SHA-256 of that declaration, so an edited .suiteward.yml is a new problem.
func problemDigest(kind, path, declaration string) string {
	text := kind + "\n" + path
	if declaration != "" {
		inner := sha256.Sum256([]byte(declaration))
		text += "\n" + hex.EncodeToString(inner[:])
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func invalidKeyOf(pull int, kind, path string, declaration ...string) string {
	return fmt.Sprintf("invalid-source:%d:%d:%s", bootRepoID, pull, problemDigest(kind, path, strings.Join(declaration, "")))
}

// linkKeyOf is the key of a link of the reachable head, whose carried declaration is the head's file with the link excluded.
func linkKeyOf(t *testing.T, pull int, link string) string {
	t.Helper()
	return invalidKeyOf(pull, "link", link, acceptingDeclaration(t, reachable, link))
}

func (w *cjJobWorld) wantInvalidSource(keys ...string) {
	w.t.Helper()
	if got := w.scopeKeys("invalid-source:"); !slices.Equal(got, keys) {
		w.t.Fatalf("invalid-source messages %v, want %v\nlogs: %s", got, keys, w.logs)
	}
}

func (w *cjJobWorld) invalidSourcePayload(index int) map[string]any {
	w.t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(w.scopeMessages("invalid-source:")[index].Payload, &payload); err != nil {
		w.t.Fatal(err)
	}
	return payload
}

func TestChecksExplainALinkTheScopeMayReachWithOneCommentPerProblem(t *testing.T) {
	w := newCJWorld(t, true)
	w.linkHead(cjHead2, "spec_link")
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(linkKeyOf(t, 5, "spec_link"))
	want := map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "kind": "link", "path": "spec_link", "declaration": acceptingDeclaration(t, reachable, "spec_link")}
	if got := w.invalidSourcePayload(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("payload %v, want %v", got, want)
	}
	if got := w.scopeMessages("invalid-source:")[0].Kind; got != githubCommentKind {
		t.Fatalf("kind %q, want %q", got, githubCommentKind)
	}

	// A push that keeps the same link is the same problem: a new check generation and no new comment.
	w.linkHead(cjHead5, "spec_link")
	w.sync(map[int]string{4: bootHead, 5: cjHead5})
	w.mustCheck()
	w.want(5, checks.Failure, "invalid-source", 2)
	w.wantInvalidSource(linkKeyOf(t, 5, "spec_link"))

	// Another bad link is another problem.
	w.linkHead(cjHead4, "other_link")
	w.sync(map[int]string{4: bootHead, 5: cjHead4})
	w.mustCheck()
	w.want(5, checks.Failure, "invalid-source", 3)
	w.wantInvalidSource(linkKeyOf(t, 5, "spec_link"), linkKeyOf(t, 5, "other_link"))
}

func TestChecksExplainAnInvalidDeclarationWithoutQuotingIt(t *testing.T) {
	w := newCJWorld(t, true)
	files := bootFiles("v2")
	files[".suiteward.yml"] = "version: 1\ninclude: [`<script>-->` EVIL\n"
	w.tree(cjHead2, files)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(invalidKeyOf(5, "declaration", ""))
	want := map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "kind": "declaration"}
	if got := w.invalidSourcePayload(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("payload %v, want %v", got, want)
	}
	if payload := string(w.scopeMessages("invalid-source:")[0].Payload); strings.Contains(payload, "EVIL") || strings.Contains(payload, "script") {
		t.Fatalf("the payload quotes the declaration: %s", payload)
	}
}

// The declaration as a link or a submodule is not a valid declaration, whatever the scope.
func TestChecksCallAnUnsafeDeclarationFileADeclarationProblem(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.repos[bootRepo].trees[cjHead2] = linkedFile{&bootTree{files: bootFiles("v2")}, ".suiteward.yml"}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(invalidKeyOf(5, "declaration", ""))
}

func TestChecksBoundThePathOfALinkAndDigestTheWholePath(t *testing.T) {
	long := strings.Repeat("l", 200) + "_link"
	w := newCJWorld(t, true)
	w.linkHead(cjHead2, long)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.wantInvalidSource(linkKeyOf(t, 5, long))
	path, _ := w.invalidSourcePayload(0)["path"].(string)
	if len(path) == 0 || len(path) > 150 || !strings.HasPrefix(path, "lll") || !strings.HasSuffix(path, "…") {
		t.Fatalf("payload path %q (%d bytes), want the path cut to at most 150 bytes", path, len(path))
	}
	message := w.scopeMessages("invalid-source:")[0]
	if len(message.Payload) > maxOutboxPayload {
		t.Fatalf("payload of %d bytes, over %d", len(message.Payload), maxOutboxPayload)
	}
	if !markerKeyPattern.MatchString(message.Key) {
		t.Fatalf("key %q is not a marker key", message.Key)
	}
}

func TestChecksWriteNoInvalidSourceCommentForAForkAClosedPullRequestOrATruncatedTree(t *testing.T) {
	t.Run("a fork", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.headRepo = map[int]string{5: "evil/widgets"}
		w.linkHead(cjHead2, "spec_link")
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "fork-unsupported", 1)
		w.wantInvalidSource()
	})
	t.Run("a closed pull request", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.linkHead(cjHead2, "spec_link")
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.sync(map[int]string{4: bootHead}) // pull 5 is no longer open

		w.mustCheck()

		w.wantNone(5)
		w.wantInvalidSource()
	})
	t.Run("a truncated tree is source-unavailable, not invalid-source", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "source-unavailable", 1)
		w.wantInvalidSource()
	})
}

// The comment is evidence: the check and its message are what they are without it. A head whose problem cannot be named, here an
// error that holds no path, publishes the same check and writes no comment, and says so in the log.
func TestChecksLeaveTheInvalidSourceCheckAsItWasWhenTheProblemCannotBeNamed(t *testing.T) {
	named := newCJWorld(t, true)
	named.linkHead(cjHead2, "spec_link")
	named.sync(map[int]string{4: bootHead, 5: cjHead2})
	unnamed := newCJWorld(t, true)
	unnamed.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: a symlink", inventory.ErrUnsafeEntry)
	unnamed.sync(map[int]string{4: bootHead, 5: cjHead2})

	named.mustCheck()
	unnamed.mustCheck()

	unnamed.wantInvalidSource()
	if fields := unnamed.logged("invalid-source comment skipped"); fields["pull"] != float64(5) {
		t.Fatalf("log fields %v, want the pull request", fields)
	}
	recordNamed, _ := named.record(5)
	recordUnnamed, _ := unnamed.record(5)
	if !reflect.DeepEqual(recordNamed, recordUnnamed) {
		t.Fatalf("check record %+v with the comment, want %+v as without it", recordNamed, recordUnnamed)
	}
	if len(named.ledger.messages) != len(unnamed.ledger.messages) {
		t.Fatalf("%d check messages with the comment, want %d", len(named.ledger.messages), len(unnamed.ledger.messages))
	}
	for i := range named.ledger.messages {
		if string(named.ledger.messages[i].Payload) != string(unnamed.ledger.messages[i].Payload) {
			t.Fatalf("check payload %s with the comment, want %s", named.ledger.messages[i].Payload, unnamed.ledger.messages[i].Payload)
		}
	}
}

// Before V1 the carrier of the bootstrap is assessed by the same code, so its invalid source gets the same comment.
func TestChecksExplainTheInvalidSourceOfTheBootstrapCarrier(t *testing.T) {
	w := newCJBootWorld(t)
	files := bootFiles("v1")
	files[".suiteward.yml"] = "version: 1\nunknown: [\n"
	w.tree(cjHead2, files)
	w.sync(map[int]string{5: cjHead2})
	w.pullProposal(5, cjHead2, bootFiles("v1"))

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(invalidKeyOf(5, "declaration", ""))
}

// D-SCOPE, an accepted link: the comment of a link carries the head's own declaration with the link's anchored path in exclude, or
// nothing when the head has no declaration (the comment then shows the one-line pattern).
func TestChecksCarryTheDeclarationThatAcceptsTheLink(t *testing.T) {
	t.Run("a head with a declaration", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.linkHead(cjHead2, "spec_link")
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		if got, want := w.invalidSourcePayload(0)["declaration"], acceptingDeclaration(t, reachable, "spec_link"); got != want {
			t.Fatalf("declaration %q, want %q", got, want)
		}
	})
	t.Run("a head without one", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].trees[cjHead2] = linkedTree{&bootTree{files: map[string]string{"src/main.go": "package main\n"}}, map[string]string{".github/workflows/ci.yml": "app"}}
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.wantInvalidSource(invalidKeyOf(5, "link", ".github/workflows/ci.yml"))
		if declaration, found := w.invalidSourcePayload(0)["declaration"]; found {
			t.Fatalf("declaration %q, want none: the head has no .suiteward.yml", declaration)
		}
	})
	t.Run("a path no exclude can name exactly", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.linkHead(cjHead2, "spec_*link")
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		if declaration, found := w.invalidSourcePayload(0)["declaration"]; found {
			t.Fatalf("declaration %q, want none: the pattern would match more than the link", declaration)
		}
	})
}

// acceptingDeclaration is raw with the anchored path of link added to exclude, as the comment of a link shows it.
func acceptingDeclaration(t *testing.T, raw, link string) string {
	t.Helper()
	accepted, err := scope.AddExcludes([]byte(raw), []string{"/" + link})
	if err != nil {
		t.Fatal(err)
	}
	return string(accepted)
}

// The head that adds the accepting exclude is assessed under the canonical scope too (inventory.Classify evaluates the head's tree
// under it), which still reaches the link.
func TestChecksAHeadThatAcceptsTheLinkIsNotInvalid(t *testing.T) {
	canonical := bootFiles("v1")
	canonical[".suiteward.yml"] = reachable
	canonical["app/main.py"] = "print('x')\n"
	canonical["app/test_main.py"] = "t\n"
	w := newCJWorldOf(t, canonical)
	head := maps.Clone(canonical)
	head[".suiteward.yml"] = acceptingDeclaration(t, reachable, "spec_link")
	w.reader.repos[bootRepo].trees[cjHead2] = linkedTree{&bootTree{files: head}, map[string]string{"spec_link": "app"}}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.wantInvalidSource()
	if record, found := w.record(5); !found || record.Reason == "invalid-source" {
		t.Fatalf("check record %+v (found %v), want a head that is assessed", record, found)
	}
}

// The comment shows the head's declaration, so a push that edits .suiteward.yml but keeps the link must post a comment with the
// current one: the key digests the carried declaration for a link, and only then.
func TestChecksPostANewLinkCommentWhenTheHeadsDeclarationChanges(t *testing.T) {
	w := newCJWorld(t, true)
	w.linkHead(cjHead2, "spec_link")
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	first := linkKeyOf(t, 5, "spec_link")

	w.linkHead(cjHead5, "spec_link") // the same file: the same problem
	w.sync(map[int]string{4: bootHead, 5: cjHead5})
	w.mustCheck()
	w.wantInvalidSource(first)

	edited := bootFiles("v1")
	edited[".suiteward.yml"] = reachable + "# edited\n"
	edited["app/main.py"] = "print('x')\n"
	w.reader.repos[bootRepo].trees[cjHead4] = linkedTree{&bootTree{files: edited}, map[string]string{"spec_link": "app"}}
	w.sync(map[int]string{4: bootHead, 5: cjHead4})
	w.mustCheck()
	w.wantInvalidSource(first, invalidKeyOf(5, "link", "spec_link", acceptingDeclaration(t, reachable+"# edited\n", "spec_link")))
}

// declarationReadFails is a tree whose declaration reads after the first one fail with err: the first is the head's own build, the
// second is the read that offers the accepting declaration.
type declarationReadFails struct {
	inventory.Tree
	err   error
	reads int
}

func (t *declarationReadFails) Read(ctx context.Context, path string) ([]byte, error) {
	if path == ".suiteward.yml" {
		if t.reads++; t.reads > 1 {
			return nil, t.err
		}
	}
	return t.Tree.Read(ctx, path)
}

func (w *cjJobWorld) linkHeadReadFailing(err error) {
	w.linkHead(cjHead2, "spec_link")
	repo := w.reader.repos[bootRepo]
	repo.trees[cjHead2] = &declarationReadFails{Tree: repo.trees[cjHead2], err: err}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
}

// A cancelled or timed-out read of the head's declaration is the run's error, so the job retries, instead of a comment without it.
func TestChecksReturnACancelledReadOfTheDeclarationThatAcceptsTheLink(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			w := newCJWorld(t, true)
			w.linkHeadReadFailing(fmt.Errorf("read the head: %w", cause))

			err := w.runChecks()

			if !errors.Is(err, cause) {
				t.Fatalf("checks run error = %v, want %v", err, cause)
			}
			w.wantInvalidSource()
		})
	}
}

// Any other failure to read it only leaves the declaration out: the check and the comment are otherwise the same, and the log says why.
func TestChecksLeaveOutTheDeclarationWhenItCannotBeRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.linkHeadReadFailing(errors.New("the blob is gone"))

	w.mustCheck()

	w.wantInvalidSource(invalidKeyOf(5, "link", "spec_link"))
	if declaration, found := w.invalidSourcePayload(0)["declaration"]; found {
		t.Fatalf("declaration %q, want none", declaration)
	}
	w.logged("declaration skipped")
}
