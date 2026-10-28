#!/bin/sh
# Writes the small test target: one module, one dependency, five tests (three pass, one fails on purpose, one skips).
# usage: smallmod.sh <dir>
set -eu
mkdir -p "$1" && cd "$1"
cat > go.mod <<'MOD'
module example.test/small

go 1.27.3

require github.com/google/uuid v1.6.0
MOD
cat > go.sum <<'SUM'
github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0=
github.com/google/uuid v1.6.0/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=
SUM
cat > small.go <<'GO'
package small

import "github.com/google/uuid"

func NewID() string { return uuid.NewString() }

func Add(a, b int) int { return a + b }
GO
cat > small_test.go <<'GO'
package small

import (
	"os"
	"testing"
	"time"
)

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("add")
	}
}

func TestNewID(t *testing.T) {
	if len(NewID()) != 36 {
		t.Fatal("uuid")
	}
}

func TestSkips(t *testing.T) { t.Skip("skipped on purpose") }

func TestFailsOnPurpose(t *testing.T) { t.Fatal("failing on purpose") }

// TestWritesTemp checks that t.TempDir() works on a read-only root (TMPDIR on the tmpfs).
func TestWritesTemp(t *testing.T) {
	if err := os.WriteFile(t.TempDir()+"/x", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
}
GO
