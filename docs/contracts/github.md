# GitHub adapter contract

- **Status:** frozen for phase M1.4 by M1.4-C0 (decisions D-GITHUB-ACCESS, D-SYNC).
- **Consumers:** M1.4-A1 (App authentication), M1.4-A2 (REST client), M1.4-A3 (typed reads and the source tree), M1.4-A4 (settings and composition), M1.4-B (reconciliation), M1.4-C (local MCP).

SuiteWard reaches GitHub outbound only, as the operator's GitHub App installed on the monitored repositories (ADR 0001); this is not the `ignisdevne[bot]` App that publishes to this repository. This document fixes the packages, types and behavior the M1.4 tasks build against. Evidence for the numbers is in [s1-github](../spikes/s1-github.md).

## Packages

- `internal/adapters/github/appauth` (A1): the App's JWT and installation tokens.
- `internal/adapters/github/rest` (A2): HTTP transport with conditional requests, the shared budget and typed errors. It takes the token as a function value (`TokenFunc`), so it never imports `appauth`; the composition (A4) adapts `(*appauth.Installation).Token` to it in one function literal.
- `internal/adapters/github` (A3): typed reads used by M1.4–M1.7 and an `inventory.Tree` over the git trees API, built on `rest`.
- `spikes/s1` may be read as a reference and is never imported; anything taken from it is redone test-first.

## Permissions (D-GITHUB-ACCESS)

The App holds Metadata read, Pull requests read, Contents read, Administration read (M1.5, protection verification) and Checks write; no Administration write, Issues or webhooks. Measured on a private repository (s1-github, permission matrix): `GET /pulls?state=open` and `GET /issues/{pr}/comments` need Pull requests read, `GET /git/trees/{sha}?recursive=1` needs Contents read, `POST /check-runs` needs Checks write. Measured 2026-10-11 by the opt-in live test (`internal/adapters/github/live_test.go`, `-tags=live`) on a private repository with a token narrowed to Metadata, Pull requests and Contents read: `GET /pulls/{n}` and `GET /commits/{sha}` answer 200, and both answer 403 to a Metadata-only token; the granted permission map equals the requested one. `PATCH /check-runs` is not measured yet; it must be asserted before the check-run write relies on it.

Every token request carries an explicit, non-empty `permissions` map and the `repositories` list (an empty map grants everything the App has). Each use requests the narrowest map: synchronization is read-only and requests `metadata`, `pull_requests` and `contents` read; protection verification requests `metadata` and `administration` read (M1.5); `checks: write` is requested only by check publication (M1.6). All tokens of one installation share the one process-wide `rest.Budget`, because the rate limit belongs to the installation.

## A1: App authentication (`appauth`)

```go
type App struct{ /* app id, RSA private key */ }
func NewApp(appID int64, privateKeyPEM []byte, clock func() time.Time) (App, error)

type Installation struct{ /* installation id, repositories, permissions, http client, base URL */ }
func (a App) Installation(id int64, repositories []string, permissions map[string]string, httpClient *http.Client, baseURL string) (*Installation, error)

// Token returns a valid installation token, minting one when none is cached
// or the cached one expires within five minutes.
func (i *Installation) Token(ctx context.Context) (Token, error)

type Token struct{ /* value, expiry, identity */ }
func (t Token) Reveal() string        // the bearer value, only for the Authorization header
func (t Token) Identity() string      // stable id of this token (not the secret), changes on rotation
func (t Token) ExpiresAt() time.Time
```

- The JWT is RS256 with the standard library: `iat` = now − 60 s, `exp` = now + 9 min, `iss` = app id. The key is PKCS#1 or PKCS#8 PEM; anything else is an error naming the format, never the content.
- Time comes only from the injected clock. Concurrent `Token` calls mint at most once.
- The token value never appears in an error, a log line, `String()`, `GoString()` or `%v` output (the same pattern as `internal/config`'s secret type). `Identity` is derived from the token (for example a SHA-256 prefix), not the value itself.
- A non-201 answer from `POST /app/installations/{id}/access_tokens` is a typed error with the status; a granted permission map narrower or wider than requested is an error.
- Tests generate their RSA key with `rsa.GenerateKey`; nothing is read from `.local/` or any PEM on disk. GitHub is an `httptest.Server`.

## A2: REST client (`rest`)

```go
// TokenFunc returns the bearer value and a stable identity of the current
// token; the identity changes when the token rotates and is never the value.
type TokenFunc func(ctx context.Context) (value, identity string, err error)

func NewClient(token TokenFunc, httpClient *http.Client, baseURL string, budget *Budget) (*Client, error)

// Get performs a conditional GET. A 304 returns the cached body with NotModified true.
func (c *Client) Get(ctx context.Context, path string) (Response, error)

// GetImmutable reads a SHA-addressed resource (git trees, blobs) with the same
// headers, budget and errors, but without the ETag cache: no If-None-Match is
// sent and nothing is stored.
func (c *Client) GetImmutable(ctx context.Context, path string) (Response, error)

type Response struct{ Status int; Body []byte; NotModified bool; ETag string }

type Budget struct{ /* shared by every client of the process */ }
func NewBudget(fraction float64, clock func() time.Time) (*Budget, error) // D-SYNC: 0.5; fraction in (0, 1]
```

`NewClient` rejects a nil `token`, `httpClient` or `budget` and a base URL that is not absolute or carries a query, a fragment or user info; its errors never print the URL.

- The ETag cache is keyed by request path and scoped to the token identity: when the identity changes, the cache is dropped (GitHub answers `Vary: Authorization`, so an ETag does not survive a rotation). Returned bodies are copies of what the cache holds. SHA-addressed reads use `GetImmutable`, so the cache only holds mutable listings.
- A response body is read up to 64 MiB; a larger one is an error, never a silent truncation.
- The budget reads `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset` of each response; requests used are `limit − remaining` as GitHub reports them, so other consumers of the same installation count. Once the requests used in the current window reach `fraction × limit`, `Get` returns `ErrBudgetExhausted` until the reset, without calling GitHub. A 304 costs nothing on GitHub's side and is not counted. Before the first response of a window the cap is unknown, so the requests already in flight pass (about one per repository at startup and after each reset). Windows are compared in server time: each response with a `Date` header within one hour of the local clock updates the offset between the local clock and GitHub's (a `Date` further off is ignored, since a window lasts at most an hour), and a window ends when the local time minus that offset reaches its reset, so a skewed clock neither disables the cap nor stops it for good. A response with the same reset refreshes the count, a later reset replaces the window, an earlier one is ignored. `BudgetExhaustedError.Reset` is in local time (reset plus offset). The budget is per process (one instance serves at most 20 repositories, D-SYNC).
- A secondary rate limit (403 or 429 with `Retry-After` while `Remaining` is above zero) is returned as `ErrRateLimited` and not fed to the budget: the caller (the reconciliation of M1.4-B) waits until `RateLimitedError.Until` before calling again. `Until` is in local time whatever its source (`X-RateLimit-Reset` and an HTTP-date `Retry-After` are shifted by the offset; a numeric `Retry-After` counts from the local clock).
- Typed errors, matched with `errors.Is`/`errors.As`: `ErrUnauthorized` (401), `ErrForbidden` (403 without rate-limit headers), `ErrNotFound` (404), `ErrUnprocessable` (422), `ErrRateLimited` (403 or 429 with `X-RateLimit-Remaining: 0` or `Retry-After` in seconds or as an HTTP date, carrying the time to wait until). Other non-2xx statuses carry the status. Bodies in errors are truncated to 512 bytes on a UTF-8 boundary and never include request headers or the token.
- Headers sent: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `If-None-Match` when cached. No retries inside the client.

## A3: typed reads (`github`)

- `OpenPulls(ctx, repo) ([]Pull, error)`: `Pull{Number, HeadSHA, HeadRef, BaseRef, BaseSHA, UpdatedAt}` from `GET /repos/{repo}/pulls?state=open&per_page=100&page=N`, pages until one has fewer than 100 items (`rest.Response` carries no headers, so `Link` is not used).
- `PullComments(ctx, repo, number, since) ([]Comment, error)`: `Comment{ID, AuthorLogin, AuthorID, Body, CreatedAt, UpdatedAt}` from `GET /repos/{repo}/issues/{n}/comments`, paged the same way, with `since` when set.
- A listing reads at most 100 pages (10,000 items); more is an invalid response, not a silent cut. Pages are not a snapshot, so each `Number` (pulls) and `ID` (comments) is returned once, the later occurrence winning.
- `Tree(ctx, repo, commitSHA) (inventory.Tree, error)`: `GET /repos/{repo}/git/trees/{sha}?recursive=1` through `GetImmutable`; mode `100644`/`100755` → `inventory.File`, `120000` → `inventory.Symlink`, `160000` → `inventory.Submodule`; `tree` entries are directories and skipped; any other mode is an error. A `truncated: true` answer is an error, never a partial tree. `Read` fetches the blob of a listed file (`GET /repos/{repo}/git/blobs/{sha}` through `GetImmutable`, base64) only when called, so only covered files are read.

## Configuration (interim, M1.4)

Settings added to the runtime contract: `GITHUB_APP_ID`, `GITHUB_APP_KEY` (secret, `_FILE` form expected), `GITHUB_INSTALLATION_ID`, `GITHUB_REPOSITORIES` (comma-separated `owner/name`, at most 20), `GITHUB_API_URL` (default `https://api.github.com`). Under D-IDENTITY (M1.5) the operator-configured installation is the trusted one; `setup` confirms that it covers every configured repository. SuiteWard does not discover installations (`GET /app/installations`) and does not trust whatever installed the App.

## M1.5: owner lookup and protection reads (normative)

Measured 2026-10-13 by `TestLiveProtectionReads` (`-tags=live`) on the S1 test repositories; results in the [M1.5 page](../plan/phases/M1.5.md).

- **Owner lookup** (`Reader.User(ctx, login) (User, error)`, synchronization token): `GET /users/{login}` returns `User{ID int64, Login string, Type string}`. Setup accepts only `Type == "User"` (an `Organization` or `Bot` is an error) and stores the numeric id; the login is display only.
- **Repository** (`Reader.Repository(ctx, repo) (Repository, error)`, synchronization token): `GET /repos/{r}` returns `Repository{ID int64, FullName string, Private bool, DefaultBranch string}`. The numeric id is the project identity (D-IDENTITY).
- **Protection** (`Reader.Protection(ctx, repo, branch) (Protection, error)`, protection token): the effective rules (`GET /repos/{r}/rules/branches/{branch}`, which GitHub limits to active rulesets at repository and organization level) and the classic protection (`GET /repos/{r}/branches/{branch}/protection`), returned as typed facts, not a verdict:
  - `Rules`: per `required_status_checks` rule, its ruleset id, each required check's context and integration id (absent is 0), and the strict flag; `FromRuleset` is true when any rule came from a ruleset;
  - `Classic`: `Absent` (404), `Unreadable` (403), or present with each required check's context and app id, the strict flag and `enforce_admins`;
  - `RulesForbidden`: the effective rules answered 403 (the private-repository-on-Free case of D-PROTECTION).

  Any other status is a `rest` error. Bodies are bounded and decoded into these types only; a decode failure names only the path. A wrapped `rest` status error can still carry part of GitHub's answer (token redacted), so callers never print or store a read error: they record `protection.Unknown()` and log only the repository and the status. The effective rules are read with `per_page=100` and followed page by page up to the listings' page bound.
- **Evaluation** is a pure function outside the adapter (the D-PROTECTION results and findings; M1.5-C2).
- **Paths:** the branch name is path-escaped (`url.PathEscape`), so a branch with `/` is read correctly.

## M1.6a: comment writes (normative)

The details are settled from the M1.6a-P0 measurement, recorded on the [M1.6a page](../plan/phases/M1.6a.md).

- **Permission.** The App holds **Pull requests: write** for acknowledgment comments, granted 2026-10-14 for the P0 measurement and confirmed by it. Only the comment publisher requests it, with the token map `{metadata: read, pull_requests: write}`. Synchronization and protection keep their read maps.
- **`rest` POST.**
  - The method is `(*Client).Post(ctx, path string, body []byte) (Response, error)`.
  - It uses no ETag cache and counts in the shared budget.
  - The body is JSON, at most 64 KiB.
  - A 201 is success.
  - A 403 or 429 carrying the secondary-limit headers, or a `Retry-After` header, is a `RateLimitedError`.
  - No request body or answer text reaches an error or a log.
- **`Writer.PostComment(ctx, repo string, number int, body string) (commentID int64, err error)`.**
  - It issues `POST /repos/{r}/issues/{number}/comments`.
  - Before posting, `Writer.CommentsByAuthor(ctx, repo, number, authorID)` lists the bot's comments, and the publisher looks for its marker in them.
- **Bot identity.** At composition, SuiteWard reads `GET /app`, which needs the App JWT and gives the slug. It then reads `GET /users/<slug>[bot]` to get the numeric account id. The command job uses that id to skip SuiteWard's own comments.
- **Not measured, not used in M1.6a.** Check runs: M1.6b measures `POST /check-runs` with `checks: write`. `PATCH` is not used.
