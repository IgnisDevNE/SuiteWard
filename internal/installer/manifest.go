package installer

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// accountPersonal is the answer for an App owned by the user's personal account.
// shortcut: an organization literally named "personal" cannot be chosen; rename the answer if that ever matters.
const accountPersonal = "personal"

const (
	manifestHome        = "https://github.com/IgnisDevNE/SuiteWard"
	defaultManifestWait = 15 * time.Minute // GitHub's code lives one hour
	maxConversionAnswer = 1 << 20
)

var (
	// A manifest code is alphanumeric (GitHub sends 20 hex characters); the shape keeps it safe in a URL path.
	codePattern    = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	ghLoginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	slugPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

	// appManifestPermissions are exactly the five of the install guide: no Contents write, no Administration write.
	appManifestPermissions = map[string]string{
		"metadata": "read", "pull_requests": "write", "contents": "read", "administration": "read", "checks": "write",
	}
)

// listenLoopback listens on 127.0.0.1 only; port 0 picks a free port.
func listenLoopback(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("the manifest port %d is outside 0-65535", port)
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen on 127.0.0.1:%d for the GitHub callback: %w", port, err)
	}
	return l, nil
}

// manifestJSON is the App manifest: no hook_attributes (the App has no webhook), private, the five permissions.
func manifestJSON(name, redirectURL string) ([]byte, error) {
	return json.Marshal(struct {
		Name               string            `json:"name"`
		URL                string            `json:"url"`
		Public             bool              `json:"public"`
		RedirectURL        string            `json:"redirect_url"`
		DefaultPermissions map[string]string `json:"default_permissions"`
	}{name, manifestHome, false, redirectURL, appManifestPermissions})
}

var manifestPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>SuiteWard</title>
<p>Sending the SuiteWard App manifest to GitHub. If nothing happens, press the button.</p>
<form id="f" method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button>Continue to GitHub</button></form>
<script>document.getElementById("f").submit()</script>
`))

// obtainManifestCode serves the manifest form on a loopback listener and returns the code, from GitHub's callback or, when that is late,
// pasted into a hidden question of the terminal (waitForCode). The code is a credential until it is converted: it is never printed.
func obtainManifestCode(ctx context.Context, env githubEnv, account string) (string, string, error) {
	if _, ok := env.R.given("manifest-code"); ok { // a code from an earlier attempt, within its hour; a secret, so never a flag
		given, err := env.R.Secret("manifest-code", "The manifest code")
		if err != nil {
			return "", "", err
		}
		code, err := parseManifestCode(string(given), "")
		return code, "", err
	}
	ln, err := listenLoopback(env.ManifestPort)
	if err != nil {
		return "", "", err
	}
	addr := ln.Addr().String()
	state := rand.Text()
	name := "suiteward-" + strings.ToLower(rand.Text()[:6])
	manifest, err := manifestJSON(name, "http://"+addr+"/callback")
	if err != nil {
		_ = ln.Close()
		return "", "", fmt.Errorf("encode the manifest: %w", err)
	}
	action := env.WebURL + "/settings/apps/new?state=" + state
	if account != accountPersonal {
		action = env.WebURL + "/organizations/" + account + "/settings/apps/new?state=" + state
	}

	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_ = manifestPage.Execute(w, struct{ Action, Manifest string }{action, string(manifest)})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || !codePattern.MatchString(code) {
			http.Error(w, "SuiteWard refused this request: its state does not match.", http.StatusBadRequest)
			return
		}
		select {
		case codes <- code:
		default: // a second callback: the first one counts
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "SuiteWard received the answer from GitHub. You can close this tab and return to the terminal.")
	})
	srv := &http.Server{
		// A request for any other host is DNS rebinding, not the browser GitHub redirected.
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != addr {
				http.Error(w, "wrong host", http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(ln) }() // the server ends with Close below
	defer func() {                    // Shutdown lets the callback's own answer reach the browser; Close only if that takes too long
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if srv.Shutdown(stop) != nil {
			_ = srv.Close()
		}
	}()

	redraw := func() {} // the page is drawn again before each question about the paste, so that the questions do not pile up on it
	if scr := env.R.screen; scr.FullScreen() {
		// What the user acts on comes first, and the explanation last: a page that is too long loses the end of it.
		blocks := []ui.Block{
			ui.Para("Open the address below in the browser of this machine and approve the App on GitHub. The installer continues by itself; if the browser shows an error after GitHub, it offers to paste the address after " + spoken(env.PasteAfter) + "."),
			ui.Fields(ui.KV{Key: "Address", Value: "http://" + addr + "/"}, ui.KV{Key: "App name", Value: name}),
		}
		if env.Tier == config.TierIsolated {
			port := addr[strings.LastIndex(addr, ":")+1:]
			blocks = append(blocks, ui.Para("Isolated host: forward the port from your computer first, then open the address on the computer.\n"+
				"aws ssm start-session --region <region> --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\""+port+"\"],\"localPortNumber\":[\""+port+"\"]}'"))
		}
		blocks = append(blocks, ui.Para("Note the App name: if the installer cannot finish, the App has to be deleted on GitHub. You may rename it there."))
		redraw = func() { scr.Page("Create the GitHub App", blocks...) }
		redraw()
	} else {
		ghSay(env.Out, "Create the GitHub App: open this address in the browser of this machine and approve the App on GitHub. The App is named %s unless you rename it there; note the name, in case the installer cannot finish and the App has to be deleted.\n  http://%s/\n", name, addr)
		if env.Tier == config.TierIsolated {
			port := addr[strings.LastIndex(addr, ":")+1:]
			ghSay(env.Out, "This host is isolated: from your computer, forward the port first, then open the address on the computer (the browser reaches the listener through the forward).\n"+
				"  aws ssm start-session --region <region> --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\"%s\"],\"localPortNumber\":[\"%s\"]}'\n", port, port)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, env.ManifestWait)
	defer cancel()
	code, err := waitForCode(ctx, env, codes, state, redraw)
	if err != nil {
		return "", "", err
	}
	return code, name, nil
}

// errNoMoreInput is the end of the input at the question that offers the paste.
var errNoMoreInput = errors.New("no more input")

// waitForCode waits for the code of GitHub's callback. It reads nothing from the terminal while it waits: a read that was still pending
// when the callback came would take the answer to the next question (the Resolver's reader is shared, and a read cannot be given up),
// or need an Enter to end it. After PasteAfter without an answer, a terminal is offered the paste of the code, or of the address the
// browser ended on (askPaste); the offer comes again after each Enter that keeps waiting. A callback that comes while it is open ends it
// (where the screen can end it without a read left on the input), or is taken when it ends. redraw draws the page again, so that the
// questions do not pile up on it.
func waitForCode(ctx context.Context, env githubEnv, codes <-chan string, state string, redraw func()) (string, error) {
	canPaste := env.R.term.IsTerminal
	for {
		if !canPaste {
			select {
			case code := <-codes:
				return code, nil
			case <-ctx.Done():
				return "", waitError(ctx, env.ManifestWait)
			}
		}
		late := time.NewTimer(env.PasteAfter)
		select {
		case code := <-codes:
			late.Stop()
			return code, nil
		case <-ctx.Done():
			late.Stop()
			return "", waitError(ctx, env.ManifestWait)
		case <-late.C:
		}
		code, err := askPaste(ctx, env, codes, state, redraw)
		switch {
		case errors.Is(err, errNoMoreInput):
			canPaste = false
		case err != nil:
			return "", err
		case code != "":
			return code, nil
		}
	}
}

// askPaste asks, hidden, for the code or the address that the browser ended on, until it gets a good one. An answer with nothing in it
// is no paste: it is the callback if that has come, and otherwise the wish to keep waiting, which is no code and no error.
// The code is a credential until it is converted: it is read with no echo, and neither the terminal nor the frame shows it. The read is
// the screen's HiddenLine, which Ctrl-C and the end of the wait (ctx) stop; the question is short, because the page it is under must
// keep the address and the command in sight on a small screen.
// GitHub's answer ends the question where the screen can end a read without leaving one on the input (HiddenLineStopsCleanly): the installer
// continues by itself, as its page says, with the code of the callback and nothing drawn from the paste. Elsewhere the read ends with an
// Enter, and the question says so.
func askPaste(ctx context.Context, env githubEnv, codes <-chan string, state string, redraw func()) (string, error) {
	scr := env.R.screen
	endsOnCallback := scr.HiddenLineStopsCleanly()
	label := "No answer from GitHub yet. Paste the address, or press Enter if the browser says SuiteWard received the answer"
	if endsOnCallback {
		label = "No answer from GitHub yet. If the browser shows an error, paste its address, or press Enter to keep waiting"
	}
	for {
		redraw()
		read, called, stop := ctx, (<-chan string)(nil), func() {}
		if endsOnCallback {
			read, called, stop = untilCallback(ctx, codes, scr.HiddenLineStopsCleanly)
		}
		secret, err := scr.HiddenLine(read, label, env.R.term.ReadPassword)
		stop()
		callback, answered := "", false
		if endsOnCallback {
			select {
			case callback, answered = <-called:
			default:
			}
		}
		switch {
		case ctx.Err() != nil: // Ctrl-C, or the time is over, as the read saw it or after it
			return "", waitError(ctx, env.ManifestWait)
		case answered:
			return callback, nil
		case errors.Is(err, io.EOF):
			ghSay(env.Out, "No more input: waiting for GitHub's callback only.\n")
			return "", errNoMoreInput
		case err != nil:
			return "", fmt.Errorf("read the pasted address: %w", err)
		}
		line := strings.TrimSpace(string(secret))
		if line == "" {
			redraw()
			select {
			case code := <-codes:
				return code, nil
			default:
				return "", nil
			}
		}
		code, perr := parseManifestCode(line, state)
		if perr == nil {
			return code, nil
		}
		label = fmt.Sprintf("That was refused: %v. Paste the address again, or press Enter", perr)
	}
}

// untilCallback is ctx that also ends when the callback brings its code, and the channel that holds that code. The question ends only while
// canEnd says that it can be ended without a read left on the input: when the screen stopped waiting for keys (a wait of it failed), the code
// waits in the channel for the Enter that ends the question, and an abandoned read never holds the keyboard from the next one. stop ends the
// watch, and must be called when the question is over: a code that came before it is in the channel, and one that came after is left in codes.
func untilCallback(ctx context.Context, codes <-chan string, canEnd func() bool) (context.Context, <-chan string, func()) {
	paste, cancel := context.WithCancel(ctx)
	called := make(chan string, 1)
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case code := <-codes:
			called <- code
			if canEnd() {
				cancel()
			}
		case <-paste.Done():
		}
	}()
	return paste, called, func() { cancel(); <-watching }
}

// spoken is a duration as the pages say it: whole minutes in words, anything else as Go writes it.
func spoken(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d > time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

func waitError(ctx context.Context, wait time.Duration) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s waiting for the manifest code from GitHub; run the installer again", wait)
	}
	return ctx.Err()
}

func ghSay(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

// parseManifestCode reads a pasted code, or a pasted callback URL. A URL that carries a state must carry the expected one;
// with no expected state (a code from an earlier attempt) the state is not checked. Errors never echo the input.
func parseManifestCode(input, state string) (string, error) {
	s := strings.TrimSpace(input)
	if strings.Contains(s, "://") || strings.Contains(s, "?") {
		u, err := url.Parse(s)
		if err != nil {
			return "", errors.New("the pasted text is not a URL")
		}
		q := u.Query()
		if got := q.Get("state"); got != "" && state != "" && subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			return "", errors.New("the pasted address carries a state that is not this run's")
		}
		s = q.Get("code")
	}
	if !codePattern.MatchString(s) {
		return "", errors.New("that is not a manifest code (letters and digits only)")
	}
	return s, nil
}

// convertedApp is what the manifest conversion returns, minus the secrets the installer has no use for.
type convertedApp struct {
	ID         int64
	Slug       string
	OwnerLogin string
	OwnerType  string
	PrivateKey []byte
}

// convertManifest exchanges the manifest code for the App (POST /app-manifests/{code}/conversions, no authentication: the
// code is the credential). It refuses an App whose permissions are not exactly the five or whose owner is not the chosen
// account; on a refusal the App exists on GitHub, so the returned app carries its slug (and no key) beside the error.
// appName is the name the manifest carried ("" for a code from an earlier attempt): an unusable answer names it, because
// the App may exist all the same.
func convertManifest(ctx context.Context, client *http.Client, apiURL, code, account, appName string) (convertedApp, error) {
	if !codePattern.MatchString(code) {
		return convertedApp{}, errors.New("the manifest code is not a code (letters and digits only)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(apiURL, "/")+"/app-manifests/"+code+"/conversions", nil)
	if err != nil {
		return convertedApp{}, errors.New("build the manifest conversion request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error // its text names the URL, which carries the code
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return convertedApp{}, fmt.Errorf("the manifest conversion request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // the answer is read below; a close error cannot change it
	if resp.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxConversionAnswer))
		hint := ""
		if resp.StatusCode == http.StatusNotFound {
			hint = " (the code was already used, or is older than one hour)"
		}
		return convertedApp{}, fmt.Errorf("the manifest conversion answered HTTP %d%s", resp.StatusCode, hint)
	}
	orphan := "" // the answer is unusable but the App may exist: its name is how to find it
	if appName != "" {
		orphan = " (the App may exist on GitHub under the name " + appName + " unless you renamed it: look for it in your GitHub App settings and delete it)"
	}
	// Only these fields are decoded: the answer also holds the client and webhook secrets.
	var out struct {
		ID    int64  `json:"id"`
		Slug  string `json:"slug"`
		Owner struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
		Permissions map[string]string `json:"permissions"`
		PEM         string            `json:"pem"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxConversionAnswer)).Decode(&out); err != nil {
		return convertedApp{}, fmt.Errorf("the manifest conversion answer is not an App object%s", orphan)
	}
	if out.ID <= 0 || !slugPattern.MatchString(out.Slug) || out.Owner.Login == "" || out.PEM == "" {
		return convertedApp{}, fmt.Errorf("the manifest conversion answer lacks the App id, slug, owner or key%s", orphan)
	}
	app := convertedApp{ID: out.ID, Slug: out.Slug, OwnerLogin: out.Owner.Login, OwnerType: out.Owner.Type}
	refuse := func(reason string) (convertedApp, error) {
		return app, fmt.Errorf("the App %s was created but %s", out.Slug, reason)
	}
	if !maps.Equal(out.Permissions, appManifestPermissions) {
		return refuse("its permissions are not exactly the five of the install guide")
	}
	if account == accountPersonal {
		if out.Owner.Type != "User" {
			return refuse("it is not owned by a personal account")
		}
	} else if out.Owner.Type != "Organization" || !strings.EqualFold(out.Owner.Login, account) {
		return refuse("it is not owned by the organization " + account)
	}
	app.PrivateKey = []byte(out.PEM)
	return app, nil
}
