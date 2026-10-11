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

The App holds Metadata read, Pull requests read, Contents read and Checks write; no Administration, Issues or webhooks. Measured on a private repository (s1-github, permission matrix): `GET /pulls?state=open` and `GET /issues/{pr}/comments` need Pull requests read, `GET /git/trees/{sha}?recursive=1` needs Contents read, `POST /check-runs` needs Checks write. Not measured with a narrowed token on a private repository: `GET /pulls/{n}`, commit reads, `PATCH /check-runs`; the opt-in live test of M1.4 asserts them first.

Every token request carries an explicit, non-empty `permissions` map and the `repositories` list (an empty map grants everything the App has). Each use requests the narrowest map: synchronization is read-only and requests `metadata`, `pull_requests` and `contents` read; `checks: write` is requested only by check publication (M1.6).

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

`NewClient` rejects a nil `token`, `httpClient` or `budget` and a base URL that is not absolute or carries a query, a fragment or user info.

- The ETag cache is keyed by request path and scoped to the token identity: when the identity changes, the cache is dropped (GitHub answers `Vary: Authorization`, so an ETag does not survive a rotation). Returned bodies are copies of what the cache holds. SHA-addressed reads use `GetImmutable`, so the cache only holds mutable listings.
- A response body is read up to 64 MiB; a larger one is an error, never a silent truncation.
- The budget reads `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset` of each response; requests used are `limit − remaining` as GitHub reports them, so other consumers of the same installation count. Once the requests used in the current window reach `fraction × limit`, `Get` returns `ErrBudgetExhausted` until the reset, without calling GitHub. A 304 costs nothing on GitHub's side and is not counted. Before the first response of a window the cap is unknown, so the requests already in flight pass (about one per repository at startup and after each reset). When the local clock passes the reset, requests are allowed again, and a later response whose reset is not earlier re-establishes the window, so a fast clock cannot disable the cap. The budget is per process (one instance serves at most 20 repositories, D-SYNC).
- A secondary rate limit (403 or 429 with `Retry-After` while `Remaining` is above zero) is returned as `ErrRateLimited` and not fed to the budget: the caller (the reconciliation of M1.4-B) waits until `RateLimitedError.Until` before calling again.
- Typed errors, matched with `errors.Is`/`errors.As`: `ErrUnauthorized` (401), `ErrForbidden` (403 without rate-limit headers), `ErrNotFound` (404), `ErrUnprocessable` (422), `ErrRateLimited` (403 or 429 with `X-RateLimit-Remaining: 0` or `Retry-After` in seconds or as an HTTP date, carrying the time to wait until). Other non-2xx statuses carry the status. Bodies in errors are truncated to 512 bytes on a UTF-8 boundary and never include request headers or the token.
- Headers sent: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `If-None-Match` when cached. No retries inside the client.

## A3: typed reads (`github`)

- `OpenPulls(ctx, repo) ([]Pull, error)`: `Pull{Number, HeadSHA, HeadRef, BaseRef, BaseSHA, UpdatedAt}` from `GET /repos/{repo}/pulls?state=open&per_page=100&page=N`, pages until one has fewer than 100 items (`rest.Response` carries no headers, so `Link` is not used).
- `PullComments(ctx, repo, number, since) ([]Comment, error)`: `Comment{ID, AuthorLogin, AuthorID, Body, CreatedAt, UpdatedAt}` from `GET /repos/{repo}/issues/{n}/comments`, paged the same way, with `since` when set.
- `Tree(ctx, repo, commitSHA) (inventory.Tree, error)`: `GET /repos/{repo}/git/trees/{sha}?recursive=1` through `GetImmutable`; mode `100644`/`100755` → `inventory.File`, `120000` → `inventory.Symlink`, `160000` → `inventory.Submodule`; `tree` entries are directories and skipped; any other mode is an error. A `truncated: true` answer is an error, never a partial tree. `Read` fetches the blob of a listed file (`GET /repos/{repo}/git/blobs/{sha}` through `GetImmutable`, base64) only when called, so only covered files are read.

## Configuration (interim, M1.4)

Settings added to the runtime contract: `GITHUB_APP_ID`, `GITHUB_APP_KEY` (secret, `_FILE` form expected), `GITHUB_INSTALLATION_ID`, `GITHUB_REPOSITORIES` (comma-separated `owner/name`, at most 20), `GITHUB_API_URL` (default `https://api.github.com`). These are an interim operator configuration: they do not establish the trusted App installation association, which D-IDENTITY and M1.5 decide. SuiteWard does not discover installations (`GET /app/installations`) and does not trust whatever installed the App.
