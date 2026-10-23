package archive

import (
	"errors"
	"io"
	"time"

	"filippo.io/age"
)

// The RED stubs: the API with no behavior, so the tests fail because the behavior is absent.

var (
	ErrInvalid  = errors.New("invalid backup")
	ErrWrongKey = errors.New("wrong key")
)

var errNotImplemented = errors.New("not implemented")

type Info struct {
	SuitewardVersion string    `json:"suitewardVersion"`
	ImageDigest      string    `json:"imageDigest"`
	Instance         string    `json:"instance"`
	Tier             string    `json:"tier"`
	CreatedAt        time.Time `json:"createdAt"`
}

type Member struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format int `json:"format"`
	Info
	Members []Member `json:"members"`
}

type Writer struct{}

func NewWriter(io.Writer, *age.X25519Recipient, Info) (*Writer, error) {
	return &Writer{}, nil
}

func (*Writer) Add(string, int64, io.Reader) error { return errNotImplemented }

func (*Writer) Close() error { return errNotImplemented }

type Reader struct{}

func Open(io.Reader, *age.X25519Identity) (*Reader, error) { return &Reader{}, nil }

func (*Reader) ReadManifest() (Manifest, error) { return Manifest{}, errNotImplemented }

func (*Reader) Extract(string) (Manifest, error) { return Manifest{}, errNotImplemented }

func NewKey() (recipient, identity string, err error) { return "", "", errNotImplemented }

func ParseRecipient(string) (*age.X25519Recipient, error) { return nil, errNotImplemented }

func ParseIdentity(string) (*age.X25519Identity, error) { return nil, errNotImplemented }

func RecoveryKit(identity, recipient, instance string) string { return "" }

func Fingerprint(recipient string) string { return "" }

func FileName(time.Time) string { return "" }
