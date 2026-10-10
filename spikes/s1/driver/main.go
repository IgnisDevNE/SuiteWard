// Command driver generates repeatable, timestamped GitHub traffic on the throwaway S1 repository so the
// orchestrator can measure what D-INTEGRATION and D-CHECKS need. It mints its own installation token and
// prints one JSON line per subcommand. It never prints a token or key material.
//
//	driver seed
//	driver open <name>
//	driver push <pr> [-outside]
//	driver comment <pr> <text>
//	driver edit <pr> <comment-id> <text>
//	driver delete <pr> <comment-id>
//	driver merge <pr> <merge|squash|rebase>
//	driver close <pr>
//
// The JWT and key handling duplicates the few lines of ../github.go: a package main cannot be imported.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// onlyRepo is the one repository the driver may write to.
const onlyRepo = "IgnisDevNE/SuiteWardQ"

const tsLayout = "2006-01-02T15:04:05.000000000Z" // the program's log format, fixed nine digits

var branchName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("github %d: %s", e.Status, e.Message) }

type driver struct {
	base, repo, appID, installationID string
	key                               *rsa.PrivateKey
	http                              *http.Client
	token                             string
	before, after                     time.Time // taken around the last request
	status                            int       // of the last request
}

func parsePrivateKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("key file holds no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("key file is neither PKCS#1 nor PKCS#8")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("key file is not an RSA key")
	}
	return rk, nil
}

func (d *driver) appJWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": d.appID})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, d.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// do performs one request with bearer auth, decodes a JSON answer into out (when non-nil) and records the
// local timestamps taken just before the request and just after the response. A status >= 300 is an *apiError.
func (d *driver) do(ctx context.Context, bearer, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "suiteward-s1-driver")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	d.before = time.Now()
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err) // names the URL, never a header
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	d.after, d.status = time.Now(), resp.StatusCode
	if err != nil {
		return fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e) // a non-JSON error body leaves the message empty
		return &apiError{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s %s: parse answer: %w", method, path, err)
		}
	}
	return nil
}

// mint gets an installation token with exactly the permissions the driver needs.
func (d *driver) mint(ctx context.Context) error {
	jwt, err := d.appJWT(time.Now())
	if err != nil {
		return err
	}
	perms := map[string]any{"permissions": map[string]string{"contents": "write", "pull_requests": "write", "issues": "write"}}
	var out struct {
		Token string `json:"token"`
	}
	if err := d.do(ctx, jwt, http.MethodPost, "/app/installations/"+d.installationID+"/access_tokens", perms, &out); err != nil {
		return fmt.Errorf("mint installation token: %w", err)
	}
	if out.Token == "" {
		return errors.New("access_tokens response has no token")
	}
	d.token = out.Token
	return nil
}

func (d *driver) api(ctx context.Context, method, path string, in, out any) error {
	if d.token == "" {
		if err := d.mint(ctx); err != nil {
			return err
		}
	}
	return d.do(ctx, d.token, method, "/repos/"+d.repo+path, in, out)
}

type commitResp struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// putFile creates a file on branch and returns the new commit sha.
func (d *driver) putFile(ctx context.Context, path, branch, content string) (string, error) {
	var out commitResp
	err := d.api(ctx, http.MethodPut, "/contents/"+path, map[string]string{
		"message": "driver: add " + path, "content": base64.StdEncoding.EncodeToString([]byte(content)), "branch": branch,
	}, &out)
	return out.Commit.SHA, err
}

// seed makes sure main has README.md and tests/a.txt. The Contents API is the route that can make the first
// commit of an empty repository; the git refs and trees endpoints refuse an empty repository.
func (d *driver) seed(ctx context.Context) (map[string]any, error) {
	var created, present []string
	for _, f := range [][2]string{{"README.md", "# SuiteWardQ\n\nThrowaway repository for the S1 spike.\n"}, {"tests/a.txt", "a\n"}} {
		err := d.api(ctx, http.MethodGet, "/contents/"+f[0]+"?ref=main", nil, nil)
		var ae *apiError
		switch {
		case err == nil:
			present = append(present, f[0])
			continue
		case !errors.As(err, &ae) || (ae.Status != http.StatusNotFound && ae.Status != http.StatusConflict):
			return nil, err // 404 is absent (also on an empty repository); anything else is a real failure
		}
		if _, err := d.putFile(ctx, f[0], "main", f[1]); err != nil {
			return nil, err
		}
		created = append(created, f[0])
	}
	return map[string]any{"created": created, "present": present}, nil
}

func (d *driver) open(ctx context.Context, name string) (map[string]any, error) {
	if !branchName.MatchString(name) {
		return nil, fmt.Errorf("name %q must match %s", name, branchName)
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := d.api(ctx, http.MethodGet, "/git/ref/heads/main", nil, &ref); err != nil {
		return nil, err
	}
	if err := d.api(ctx, http.MethodPost, "/git/refs", map[string]string{"ref": "refs/heads/" + name, "sha": ref.Object.SHA}, nil); err != nil {
		return nil, err
	}
	if _, err := d.putFile(ctx, "tests/"+name+".txt", name, name+"\n"); err != nil {
		return nil, err
	}
	var pr struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := d.api(ctx, http.MethodPost, "/pulls", map[string]string{"title": name, "head": name, "base": "main"}, &pr); err != nil {
		return nil, err
	}
	return map[string]any{"pr": pr.Number, "branch": name, "base_sha": ref.Object.SHA, "head_sha": pr.Head.SHA}, nil
}

func (d *driver) push(ctx context.Context, pr int, outside bool) (map[string]any, error) {
	var p struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := d.api(ctx, http.MethodGet, fmt.Sprintf("/pulls/%d", pr), nil, &p); err != nil {
		return nil, err
	}
	dir := "tests"
	if outside {
		dir = "other"
	}
	path := fmt.Sprintf("%s/push-%d.txt", dir, time.Now().UnixNano())
	sha, err := d.putFile(ctx, path, p.Head.Ref, path+"\n")
	if err != nil {
		return nil, err
	}
	return map[string]any{"pr": pr, "path": path, "outside": outside, "branch": p.Head.Ref, "head_sha": sha}, nil
}

func (d *driver) comment(ctx context.Context, pr int, text string) (map[string]any, error) {
	var out struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
	}
	if err := d.api(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", pr), map[string]string{"body": text}, &out); err != nil {
		return nil, err
	}
	return map[string]any{"pr": pr, "comment_id": out.ID, "created_at": out.CreatedAt}, nil
}

func (d *driver) edit(ctx context.Context, id int64, text string) (map[string]any, error) {
	var out struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := d.api(ctx, http.MethodPatch, fmt.Sprintf("/issues/comments/%d", id), map[string]string{"body": text}, &out); err != nil {
		return nil, err
	}
	return map[string]any{"comment_id": id, "updated_at": out.UpdatedAt}, nil
}

func (d *driver) delete(ctx context.Context, id int64) (map[string]any, error) {
	if err := d.api(ctx, http.MethodDelete, fmt.Sprintf("/issues/comments/%d", id), nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"comment_id": id}, nil
}

func (d *driver) merge(ctx context.Context, pr int, method string) (map[string]any, error) {
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := d.api(ctx, http.MethodPut, fmt.Sprintf("/pulls/%d/merge", pr), map[string]string{"merge_method": method}, &out); err != nil {
		return nil, err
	}
	return map[string]any{"pr": pr, "requested_method": method, "merge_commit_sha": out.SHA, "merged": out.Merged}, nil
}

func (d *driver) close(ctx context.Context, pr int) (map[string]any, error) {
	if err := d.api(ctx, http.MethodPatch, fmt.Sprintf("/pulls/%d", pr), map[string]string{"state": "closed"}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"pr": pr}, nil
}

func atoi(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive number", s)
	}
	return n, nil
}

const usage = "usage: driver seed | open <name> | push <pr> [-outside] | comment <pr> <text> | edit <pr> <comment-id> <text> | delete <pr> <comment-id> | merge <pr> <merge|squash|rebase> | close <pr>"

// dispatch validates the arguments of one subcommand and runs it. Nothing is requested before they are valid.
func (d *driver) dispatch(ctx context.Context, cmd string, a []string, outside bool) (map[string]any, error) {
	// minimum arguments; comment and edit take the rest as text, the others take exactly this many
	least, ok := map[string]int{"seed": 0, "open": 1, "push": 1, "close": 1, "comment": 2, "edit": 3, "delete": 2, "merge": 2}[cmd]
	if !ok || len(a) < least || (len(a) > least && cmd != "comment" && cmd != "edit") {
		return nil, errors.New(usage)
	}
	if outside && cmd != "push" {
		return nil, errors.New("-outside belongs to push")
	}
	switch cmd {
	case "seed":
		return d.seed(ctx)
	case "open":
		return d.open(ctx, a[0])
	}
	pr, err := atoi(a[0])
	if err != nil {
		return nil, err
	}
	switch cmd {
	case "push":
		return d.push(ctx, pr, outside)
	case "close":
		return d.close(ctx, pr)
	case "comment":
		return d.comment(ctx, pr, strings.Join(a[1:], " "))
	case "merge":
		if m := a[1]; m != "merge" && m != "squash" && m != "rebase" {
			return nil, fmt.Errorf("merge method %q is not merge, squash or rebase", m)
		}
		return d.merge(ctx, pr, a[1])
	}
	// edit and delete address the comment by id; <pr> is kept for symmetry with the other commands.
	id, err := strconv.ParseInt(a[1], 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("%q is not a comment id", a[1])
	}
	if cmd == "delete" {
		return d.delete(ctx, id)
	}
	return d.edit(ctx, id, strings.Join(a[2:], " "))
}

// run executes one subcommand and prints its JSON line to out. The repository is checked before the key is read.
func run(args []string, getenv func(string) string, out io.Writer) error {
	if repo := getenv("S1_REPO"); repo != onlyRepo {
		return fmt.Errorf("S1_REPO is %q: the driver only writes to %s", repo, onlyRepo)
	}
	// -outside may sit anywhere after the subcommand.
	outside := false
	var rest []string
	for _, a := range args {
		if a == "-outside" {
			outside = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return errors.New(usage)
	}
	for _, name := range []string{"S1_APP_ID", "S1_INSTALLATION_ID"} {
		if getenv(name) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	keyFile, base := getenv("S1_KEY_FILE"), getenv("S1_API_BASE")
	if keyFile == "" {
		keyFile = "/run/secrets/app-key"
	}
	if base == "" {
		base = "https://api.github.com"
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read key file: %w", err)
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	d := &driver{base: strings.TrimRight(base, "/"), repo: onlyRepo, appID: getenv("S1_APP_ID"), installationID: getenv("S1_INSTALLATION_ID"), key: key, http: &http.Client{Timeout: 30 * time.Second}}

	fields, err := d.dispatch(context.Background(), rest[0], rest[1:], outside)
	if err != nil {
		return err
	}
	fields["cmd"], fields["status"] = rest[0], d.status
	// The timestamps bracket the last request, the one that changed (or read) what the command is about.
	fields["t_before"], fields["t_after"] = d.before.UTC().Format(tsLayout), d.after.UTC().Format(tsLayout)
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", b)
	return err
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "driver:", err)
		os.Exit(1)
	}
}
