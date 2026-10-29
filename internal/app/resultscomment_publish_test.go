package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The publisher of the three comments of a head (docs/contracts/runtime.md, "The comments of a head", "Payloads"): each kind has its key, its strict payload and its
// renderer, and the renderer checks the payload against the key. The payloads below are written out as JSON, so that the tests pin the wire.

const (
	rcHead = "0123456789abcdef0123456789abcdef01234567" // its short form is 0123456
	rcBase = "http://127.0.0.1:8080"
)

func rcCommon(extra map[string]any) map[string]any {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "head": rcHead, "tier": "isolated"}
	maps.Copy(payload, extra)
	return payload
}

var (
	rcCanonicalCounts = map[string]any{"tests": 12, "passed": 9, "skipped": 3, "failed": 0, "missing": 0, "flaky": 0, "excluded": 0}
	rcReceiptCounts   = map[string]any{"detects": 2, "inert": 1, "newApi": 0, "fails": 0, "missing": 0, "flaky": 0, "skipped": 0, "excluded": 0, "markers": 0, "unparsed": 0}
)

// rcResults is a clean results payload, jobs 11 and 12, with the key that names them, after edit.
func rcResults(edit func(map[string]any)) (key string, payload map[string]any) {
	payload = rcCommon(map[string]any{
		"canonicalJob": 11, "receiptJob": 12, "reference": "P2-R2", "canonicalLevel": "required", "receiptLevel": "required",
		"canonical": map[string]any{"status": "complete", "counts": maps.Clone(rcCanonicalCounts)},
		"receipt":   map[string]any{"status": "complete", "counts": maps.Clone(rcReceiptCounts)},
		"weakened":  0, "next": "none",
	})
	if edit != nil {
		edit(payload)
	}
	return fmt.Sprintf("results:%d:%d:%s:%d-%d", cmtRepoID, cmtPull, rcHead, 11, 12), payload
}

func (w *cmtWorld) deliverPayload(key string, payload any) error {
	w.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.pub.publish(w.t.Context(), key, raw)
}

func (w *cmtWorld) mustPost(key string, payload any) string {
	w.t.Helper()
	if err := w.deliverPayload(key, payload); err != nil {
		w.t.Fatalf("publish %s: %v", key, err)
	}
	posts := w.writer.posts()
	if len(posts) != 1 {
		w.t.Fatalf("posted %d comments, want 1", len(posts))
	}
	if !commandtext.HasMarker(posts[0].body, cmtInstance, key) || posts[0].repo != cmtRepo || posts[0].number != cmtPull {
		w.t.Fatalf("the comment is on %s#%d, or does not end with the marker of %s:\n%s", posts[0].repo, posts[0].number, key, posts[0].body)
	}
	return posts[0].body
}

func TestTheResultsCommentIsPostedFromItsPayloadWithTheLinkOfThePullRequestPage(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.reportBase = rcBase
	key, payload := rcResults(nil)
	body := w.mustPost(key, payload)
	for _, want := range []string{
		"SuiteWard ran the tests for this commit (`0123456`). Nothing needs your attention.",
		"Approved tests: 12 tests ran. 9 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 3 were skipped.",
		"Tests this change adds or edits: 3.\n- Fail before the change and pass now, so they show the change works: 2",
		"- `fix`: met.", "Nothing to do for now.",
		"Details: [pull request page](http://127.0.0.1:8080/pulls/acme/widgets/7).",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, body)
		}
	}
}

func TestTheResultsCommentWithoutABaseAddressSaysSo(t *testing.T) {
	w := newCmtWorld(t)
	key, payload := rcResults(nil)
	body := w.mustPost(key, payload)
	if !strings.Contains(body, "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts.") || strings.Contains(body, "Details:") {
		t.Errorf("the comment of an instance with no base address:\n%s", body)
	}
}

// A link that the renderer refuses drops the link and never the comment: the results are what the owner reads.
func TestTheResultsCommentKeepsItsTextWhenItsLinkIsRefused(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1:8080/x", "http://127.0.0.1:8080 "} {
		w := newCmtWorld(t)
		w.pub.reportBase = base
		key, payload := rcResults(nil)
		body := w.mustPost(key, payload)
		if !strings.Contains(body, "Nothing needs your attention.") || strings.Contains(body, "Details:") || !strings.Contains(body, "has no web address set") {
			t.Errorf("base %q: the text was lost or kept a refused link:\n%s", base, body)
		}
		if !strings.Contains(w.logs.String(), "link") {
			t.Errorf("base %q: the refused link is not logged: %s", base, w.logs.String())
		}
	}
}

func TestTheResultsCommentStatesAnUnavailableRunAndTheOffersOfItsPayload(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.reportBase = rcBase
	key, payload := rcResults(func(p map[string]any) {
		p["canonical"] = map[string]any{"status": "unavailable", "reason": "no-executor"}
		p["unblocked"], p["ended"], p["next"] = []string{"canonical"}, []string{"canonical"}, "approve"
		p["approveMode"], p["approvalMissing"] = true, true
	})
	body := w.mustPost(key, payload)
	for _, want := range []string{
		"SuiteWard ran some of the tests for this commit (`0123456`). Something needs your attention.",
		"Approved tests: not run. The test runner did not respond.",
		"The owner unblocked this commit, so its checks do not wait for the approved tests. SuiteWard had stopped waiting for the test runner, which was not responding. After the owner approves this revision,",
		"```\n/suiteward approve P2-R2 <fix|feature|refactor> <code>\n```",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, body)
		}
	}
}

// Where the receipt holds the approve too the next step is the unblock, and the comment still says what an approval does to the approved tests that did not run.
func TestTheResultsCommentSaysWhatAnApprovalDoesWhenTheUnblockIsTheNextStep(t *testing.T) {
	for name, approvalMissing := range map[string]bool{"an approval is missing": true, "an approval is not missing": false} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			key, payload := rcResults(func(p map[string]any) {
				p["canonical"] = map[string]any{"status": "unavailable", "reason": "timeout"}
				p["receipt"] = map[string]any{"status": "unavailable", "reason": "timeout"}
				p["next"] = "unblock"
				if approvalMissing {
					p["approvalMissing"] = true
				}
			})
			body := w.mustPost(key, payload)
			if !strings.Contains(body, "/suiteward unblock <code>") {
				t.Errorf("the comment does not offer the unblock:\n%s", body)
			}
			if got := strings.Contains(body, "After the owner approves this revision, SuiteWard runs the approved tests again"); got != approvalMissing {
				t.Errorf("the sentence about the approval is present = %v, want %v:\n%s", got, approvalMissing, body)
			}
		})
	}
}

func TestTheResultsCommentOfTheCoLocatedTierSaysSo(t *testing.T) {
	w := newCmtWorld(t)
	key, payload := rcResults(func(p map[string]any) { p["tier"] = "co-located" })
	if body := w.mustPost(key, payload); !strings.Contains(body, commandtext.CoLocatedNotice) {
		t.Errorf("the comment of a co-located instance lacks the notice:\n%s", body)
	}
}

func TestTheResultsCommentRefusesAPayloadThatDoesNotMatchItsKeyOrItself(t *testing.T) {
	complete := func(counts map[string]any) map[string]any {
		return map[string]any{"status": "complete", "counts": counts}
	}
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		key  func(string) string // changes the key of the clean payload
	}{
		"another head in the key":                  {nil, func(k string) string { return strings.Replace(k, rcHead, strings.Repeat("a", 40), 1) }},
		"another pull request in the key":          {nil, func(k string) string { return strings.Replace(k, ":7:", ":8:", 1) }},
		"another repository in the key":            {nil, func(k string) string { return strings.Replace(k, ":1001:", ":1002:", 1) }},
		"another canonical job in the key":         {nil, func(k string) string { return strings.Replace(k, ":11-12", ":13-12", 1) }},
		"another receipt job in the key":           {nil, func(k string) string { return strings.Replace(k, ":11-12", ":11-13", 1) }},
		"a key with no jobs":                       {nil, func(k string) string { return strings.TrimSuffix(k, ":11-12") }},
		"a key with a head and no pull":            {nil, func(k string) string { return strings.Replace(k, ":7:", ":", 1) }},
		"a key with one more part":                 {nil, func(k string) string { return k + ":x" }},
		"a repository that is not owner/name":      {func(p map[string]any) { p["repo"] = "widgets" }, nil},
		"a head that is not 40 hexadecimal digits": {func(p map[string]any) { p["head"] = "0123456" }, nil},
		"an unknown tier":                          {func(p map[string]any) { p["tier"] = "cloud" }, nil},
		"both jobs 0": {func(p map[string]any) {
			p["canonicalJob"], p["receiptJob"], p["canonical"], p["receipt"], p["canonicalLevel"], p["receiptLevel"] = 0, 0, nil, nil, "off", "off"
		}, func(k string) string { return strings.Replace(k, ":11-12", ":0-0", 1) }},
		"a negative job":                                 {func(p map[string]any) { p["receiptJob"] = -1 }, nil},
		"a canonical run with no job":                    {func(p map[string]any) { p["canonicalJob"] = 0 }, nil},
		"a job and no receipt":                           {func(p map[string]any) { delete(p, "receipt") }, nil},
		"a canonical run under a level that is off":      {func(p map[string]any) { p["canonicalLevel"] = "off" }, nil},
		"no canonical run under a level that is not off": {func(p map[string]any) { delete(p, "canonical"); p["canonicalJob"] = 0 }, func(k string) string { return strings.Replace(k, ":11-12", ":0-12", 1) }},
		"an unknown level":                               {func(p map[string]any) { p["receiptLevel"] = "mandatory" }, nil},
		"a reference that is not P<n>-R<m>":              {func(p map[string]any) { p["reference"] = "P2-R" }, nil},
		"an unknown status":                              {func(p map[string]any) { p["canonical"] = map[string]any{"status": "cancelled"} }, nil},
		"a complete run with no counts":                  {func(p map[string]any) { p["canonical"] = map[string]any{"status": "complete"} }, nil},
		"a complete run with a reason": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "complete", "reason": "timeout", "counts": rcCanonicalCounts}
		}, nil},
		"an unavailable run with counts": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "unavailable", "reason": "timeout", "counts": rcCanonicalCounts}
		}, nil},
		"an unavailable run with no reason": {func(p map[string]any) { p["canonical"] = map[string]any{"status": "unavailable"} }, nil},
		"a reason that is no code of the closed set": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "unavailable", "reason": cmtLeakText}
		}, nil},
		"canonical counts that do not add up": {func(p map[string]any) {
			p["canonical"] = complete(map[string]any{"tests": 5, "passed": 1, "skipped": 0, "failed": 0, "missing": 0, "flaky": 0, "excluded": 0})
		}, nil},
		"a canonical count that is missing": {func(p map[string]any) {
			counts := maps.Clone(rcCanonicalCounts)
			delete(counts, "excluded")
			p["canonical"] = complete(counts)
		}, nil},
		"a receipt count that is missing": {func(p map[string]any) {
			counts := maps.Clone(rcReceiptCounts)
			delete(counts, "unparsed")
			p["receipt"] = complete(counts)
		}, nil},
		"a negative receipt count": {func(p map[string]any) {
			counts := maps.Clone(rcReceiptCounts)
			counts["fails"] = -1
			p["receipt"] = complete(counts)
		}, nil},
		"a count of another run in the receipt":                      {func(p map[string]any) { p["receipt"] = complete(rcCanonicalCounts) }, nil},
		"a negative weakened count":                                  {func(p map[string]any) { p["weakened"] = -1 }, nil},
		"an unknown next step":                                       {func(p map[string]any) { p["next"] = "wait" }, nil},
		"no next step":                                               {func(p map[string]any) { delete(p, "next") }, nil},
		"an approve with no reference":                               {func(p map[string]any) { p["next"], p["reference"], p["approvalMissing"] = "approve", "", true }, nil},
		"an approve for a head whose approval is not missing":        {func(p map[string]any) { p["next"] = "approve" }, nil},
		"a refused approve for a head whose approval is not missing": {func(p map[string]any) { p["next"] = "blocked" }, nil},
		"an approval that is missing and nothing to do":              {func(p map[string]any) { p["approvalMissing"] = true }, nil},
		"an approval that is missing and a fix to push":              {func(p map[string]any) { p["approvalMissing"], p["next"] = true, "fix" }, nil},
		"an approval that is not a boolean":                          {func(p map[string]any) { p["approvalMissing"] = "yes" }, nil},
		"a mode for a step that is no approve":                       {func(p map[string]any) { p["approveMode"] = true }, nil},
		"an unblocked run that is complete":                          {func(p map[string]any) { p["unblocked"] = []string{"canonical"} }, nil},
		"an unblocked plan that is no plan":                          {func(p map[string]any) { p["unblocked"] = []string{"both"} }, nil},
		"an unblocked plan twice": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "unavailable", "reason": "timeout"}
			p["unblocked"] = []string{"canonical", "canonical"}
		}, nil},
		"unblocked plans in the wrong order": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "unavailable", "reason": "timeout"}
			p["receipt"] = map[string]any{"status": "unavailable", "reason": "timeout"}
			p["unblocked"] = []string{"receipt", "canonical"}
		}, nil},
		"an end that is no unblock": {func(p map[string]any) {
			p["canonical"] = map[string]any{"status": "unavailable", "reason": "timeout"}
			p["unblocked"], p["ended"] = []string{}, []string{"canonical"}
		}, nil},
		"an unknown field": {func(p map[string]any) { p["text"] = cmtLeakText }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			key, payload := rcResults(tc.edit)
			if tc.key != nil {
				key = tc.key(key)
			}
			err := w.deliverPayload(key, payload)
			if err == nil || len(w.writer.posts()) != 0 {
				t.Fatalf("publish = %v with %d posts, want a refusal and no post", err, len(w.writer.posts()))
			}
			if strings.Contains(err.Error(), cmtLeakText) {
				t.Errorf("the error quotes the payload: %v", err)
			}
		})
	}
}

func TestTheResultsCommentRefusesAPayloadWithATrailingValue(t *testing.T) {
	w := newCmtWorld(t)
	key, payload := rcResults(nil)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.pub.publish(t.Context(), key, append(raw, []byte(` {"x":1}`)...)); err == nil || len(w.writer.posts()) != 0 {
		t.Errorf("publish = %v with %d posts, want a refusal and no post", err, len(w.writer.posts()))
	}
}

func rcNotResponding(edit func(map[string]any)) (key string, payload map[string]any) {
	payload = rcCommon(map[string]any{"canonicalJob": 11, "receiptJob": 0, "unblock": true})
	if edit != nil {
		edit(payload)
	}
	return fmt.Sprintf("not-responding:%d:%d:%s:%d-%d", cmtRepoID, cmtPull, rcHead, 11, 0), payload
}

func TestTheNotRespondingCommentIsPostedFromItsPayload(t *testing.T) {
	for name, unblock := range map[string]bool{"with the offer": true, "without the offer": false} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.pub.reportBase = rcBase
			key, payload := rcNotResponding(func(p map[string]any) { p["unblock"] = unblock })
			body := w.mustPost(key, payload)
			for _, want := range []string{"**The test runner is not responding.**", "The tests for this commit (`0123456`) are waiting.", "Details: [pull request page](http://127.0.0.1:8080/pulls/acme/widgets/7)."} {
				if !strings.Contains(body, want) {
					t.Errorf("the comment lacks %q:\n%s", want, body)
				}
			}
			if got := strings.Contains(body, "/suiteward unblock <code>"); got != unblock {
				t.Errorf("the offer is present = %v, want %v:\n%s", got, unblock, body)
			}
		})
	}
}

func TestTheNotRespondingCommentRefusesAPayloadThatDoesNotMatchItsKeyOrItself(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		key  func(string) string
	}{
		"another head in the key":         {nil, func(k string) string { return strings.Replace(k, rcHead, strings.Repeat("a", 40), 1) }},
		"another job in the key":          {nil, func(k string) string { return strings.Replace(k, ":11-0", ":12-0", 1) }},
		"another pull request in the key": {nil, func(k string) string { return strings.Replace(k, ":7:", ":8:", 1) }},
		"both jobs 0":                     {func(p map[string]any) { p["canonicalJob"] = 0 }, func(k string) string { return strings.Replace(k, ":11-0", ":0-0", 1) }},
		"a negative job":                  {func(p map[string]any) { p["receiptJob"] = -1 }, func(k string) string { return strings.Replace(k, ":11-0", ":11--1", 1) }},
		"an unknown tier":                 {func(p map[string]any) { p["tier"] = "cloud" }, nil},
		"an unknown field":                {func(p map[string]any) { p["text"] = cmtLeakText }, nil},
		"a head that is short":            {func(p map[string]any) { p["head"] = "0123456" }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			key, payload := rcNotResponding(tc.edit)
			if tc.key != nil {
				key = tc.key(key)
			}
			err := w.deliverPayload(key, payload)
			if err == nil || len(w.writer.posts()) != 0 {
				t.Fatalf("publish = %v with %d posts, want a refusal and no post", err, len(w.writer.posts()))
			}
			if strings.Contains(err.Error(), cmtLeakText) {
				t.Errorf("the error quotes the payload: %v", err)
			}
		})
	}
}

func rcRunnerBack(edit func(map[string]any)) (key string, payload map[string]any) {
	payload = rcCommon(nil)
	if edit != nil {
		edit(payload)
	}
	return fmt.Sprintf("runner-back:%d:%d:%s", cmtRepoID, cmtPull, rcHead), payload
}

func TestTheRunnerBackCommentIsPostedFromItsPayload(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.reportBase = rcBase
	key, payload := rcRunnerBack(nil)
	body := w.mustPost(key, payload)
	for _, want := range []string{"**The test runner is back.**", "The owner unblocked this commit (`0123456`)", "Details: [pull request page](http://127.0.0.1:8080/pulls/acme/widgets/7)."} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, body)
		}
	}
}

func TestTheRunnerBackCommentRefusesAPayloadThatDoesNotMatchItsKeyOrItself(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		key  func(string) string
	}{
		"another head in the key":         {nil, func(k string) string { return strings.Replace(k, rcHead, strings.Repeat("a", 40), 1) }},
		"another pull request in the key": {nil, func(k string) string { return strings.Replace(k, ":7:", ":8:", 1) }},
		"a key with jobs":                 {nil, func(k string) string { return k + ":11-12" }},
		"a key with no head":              {nil, func(k string) string { return strings.TrimSuffix(k, ":"+rcHead) }},
		"an unknown tier":                 {func(p map[string]any) { p["tier"] = "cloud" }, nil},
		"an unknown field":                {func(p map[string]any) { p["jobs"] = "11-12" }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			key, payload := rcRunnerBack(tc.edit)
			if tc.key != nil {
				key = tc.key(key)
			}
			if err := w.deliverPayload(key, payload); err == nil || len(w.writer.posts()) != 0 {
				t.Fatalf("publish = %v with %d posts, want a refusal and no post", err, len(w.writer.posts()))
			}
		})
	}
}

// Each key posts once: the marker that the bot's own comment holds is the idempotency key, as for every comment (the fake writer lists what it posted).
func TestTheThreeCommentsPostOncePerKey(t *testing.T) {
	w := newCmtWorld(t)
	for _, build := range []func() (string, map[string]any){
		func() (string, map[string]any) { return rcResults(nil) },
		func() (string, map[string]any) { return rcNotResponding(nil) },
		func() (string, map[string]any) { return rcRunnerBack(nil) },
	} {
		key, payload := build()
		before := len(w.writer.posts())
		for range 2 {
			if err := w.deliverPayload(key, payload); err != nil {
				t.Fatalf("publish %s: %v", key, err)
			}
		}
		if got := len(w.writer.posts()) - before; got != 1 {
			t.Errorf("%s: posted %d times, want once", key, got)
		}
	}
}

func TestTheKeysOfTheThreeCommentsHaveTheirParts(t *testing.T) {
	head := rcHead
	for key, want := range map[string]bool{
		"results:1001:7:" + head + ":11-12":       true,
		"results:1001:7:" + head + ":0-12":        true,
		"results:1001:7:" + head:                  false,
		"results:1001:7:" + head + ":11-12:x":     false,
		"not-responding:1001:7:" + head + ":11-0": true,
		"not-responding:1001:7:" + head:           false,
		"runner-back:1001:7:" + head:              true,
		"runner-back:1001:7":                      false,
		"runner-back:1001:7:" + head + ":11-0":    false,
		"results:0:7:" + head + ":11-12":          false,
		"results:01001:7:" + head + ":11-12":      false,
	} {
		if _, _, _, ok := splitKey(key); ok != want {
			t.Errorf("splitKey(%q) ok = %v, want %v", key, ok, want)
		}
	}
}
