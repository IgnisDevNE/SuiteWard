package goprofile

import (
	"slices"
	"testing"
)

func TestMarkers(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []Marker
	}{
		"testing imported outside a test file": {
			"package p\n\nimport \"testing\"\n\nvar _ = testing.Verbose\n",
			[]Marker{{"testing-import", "p.go", 3}},
		},
		"testing.Testing": {
			"package p\n\nimport \"testing\"\n\nfunc f() bool {\n\treturn testing.Testing()\n}\n",
			[]Marker{{"testing-import", "p.go", 3}, {"testing-testing", "p.go", 6}},
		},
		"testing under another name": {
			"package p\n\nimport t \"testing\"\n\nfunc f() bool { return t.Testing() }\n",
			[]Marker{{"testing-import", "p.go", 3}, {"testing-testing", "p.go", 5}},
		},
		"flag.Lookup of a test flag": {
			"package p\n\nimport \"flag\"\n\nfunc f() {\n\t_ = flag.Lookup(\"test.v\")\n\t_ = flag.Lookup(\"verbose\")\n\t_ = flag.Lookup(`test.timeout`)\n}\n",
			[]Marker{{"flag-lookup-test", "p.go", 6}, {"flag-lookup-test", "p.go", 8}},
		},
		"os.Exit inside init, closures included": {
			"package p\n\nimport \"os\"\n\nfunc init() {\n\tif len(os.Args) > 5 {\n\t\tos.Exit(1)\n\t}\n\tfunc() { os.Exit(2) }()\n}\n\nfunc main() {\n\tos.Exit(3)\n}\n",
			[]Marker{{"exit-in-init", "p.go", 7}, {"exit-in-init", "p.go", 9}},
		},
		"a method called init is not init": {
			"package p\n\nimport \"os\"\n\ntype s struct{}\n\nfunc (s) init() { os.Exit(1) }\n",
			nil,
		},
		"reads of TESTING and CI": {
			"package p\n\nimport \"os\"\n\nfunc f() {\n\t_ = os.Getenv(\"CI\")\n\t_, _ = os.LookupEnv(\"TESTING\")\n\t_ = os.Getenv(\"HOME\")\n\t_ = os.Getenv(\"ci\")\n}\n",
			[]Marker{{"env-read", "p.go", 6}, {"env-read", "p.go", 7}},
		},
		"os under another name, on several lines": {
			"package p\n\nimport o \"os\"\n\nfunc f() {\n\t_ = o.Getenv(\n\t\t\"CI\",\n\t)\n}\n",
			[]Marker{{"env-read", "p.go", 6}},
		},
		"names of test files": {
			"package p\n\nimport \"os\"\n\nfunc f() {\n\t_, _ = os.ReadFile(\"a_test.go\")\n\t_ = \"plain.go\"\n\t_ = `x_test.go`\n}\n",
			[]Marker{{"test-file-read", "p.go", 6}, {"test-file-read", "p.go", 8}},
		},
		"the same marker twice on a line": {
			"package p\n\nimport \"os\"\n\nvar _ = os.Getenv(\"CI\") + os.Getenv(\"CI\")\n",
			[]Marker{{"env-read", "p.go", 5}},
		},
		"nothing of the kind": {
			"package p\n\nimport (\n\t\"flag\"\n\t\"os\"\n)\n\nfunc init() { flag.Parse() }\n\nfunc f() { os.Exit(0) }\n",
			nil,
		},
		// The head's build failure shows in the receipt; the markers of what parsed still count.
		"a file that does not parse": {
			"package p\n\nimport \"testing\"\n\nfunc f( {\n",
			[]Marker{{"testing-import", "p.go", 3}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Markers(map[string][]byte{"p.go": []byte(c.src)}); !slices.Equal(got, c.want) {
				t.Errorf("got %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestMarkersLookOnlyAtNonTestGoFiles(t *testing.T) {
	const everything = "package p\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc init() { os.Exit(testingLine()) }\n\nfunc testingLine() int {\n\t_ = os.Getenv(\"CI\")\n\treturn len(os.Args[0] + \"a_test.go\")\n}\n\nvar _ = testing.Testing\n"
	got := Markers(map[string][]byte{
		"a_test.go":     []byte(everything),
		"dir/b_test.go": []byte(everything),
		"README.md":     []byte(everything),
		"script.sh":     []byte(everything),
		"dir/helper.go": []byte("package p\n\nimport \"os\"\n\nvar _ = os.Getenv(\"CI\")\n"),
	})
	want := []Marker{{"env-read", "dir/helper.go", 5}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMarkersAreSortedByPathThenLine(t *testing.T) {
	const two = "package p\n\nimport \"os\"\n\nvar a = os.Getenv(\"TESTING\")\nvar b = os.Getenv(\"CI\")\n"
	got := Markers(map[string][]byte{"z/z.go": []byte(two), "a/a.go": []byte(two)})
	want := []Marker{{"env-read", "a/a.go", 5}, {"env-read", "a/a.go", 6}, {"env-read", "z/z.go", 5}, {"env-read", "z/z.go", 6}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if got := Markers(nil); len(got) != 0 {
		t.Errorf("no files gave %v", got)
	}
}

// The position of a marker is the line of the file, whatever //line directives the file holds: a candidate must not be
// able to push a marker to a line the wire refuses, or hide it at line 1.
func TestMarkersAreAtTheLineOfTheFileNotOfALineDirective(t *testing.T) {
	const source = "package p\n\nimport \"os\"\n\n//line other.go:99999999\nvar a = os.Getenv(\"CI\")\n//line other.go:1\nvar b = os.Getenv(\"TESTING\")\n/*line :7:1*/ var c = os.Getenv(\"CI\")\n"
	got := Markers(map[string][]byte{"p.go": []byte(source)})
	want := []Marker{{"env-read", "p.go", 6}, {"env-read", "p.go", 8}, {"env-read", "p.go", 9}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMarkersFlagExitsThatInitIsNotNeededFor(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []Marker
	}{
		"syscall.Exit anywhere": {
			"package p\n\nimport \"syscall\"\n\nfunc f() {\n\tsyscall.Exit(1)\n}\n",
			[]Marker{{"exit-in-init", "p.go", 6}},
		},
		"syscall.Exit under another name": {
			"package p\n\nimport sc \"syscall\"\n\nfunc f() { sc.Exit(1) }\n",
			[]Marker{{"exit-in-init", "p.go", 5}},
		},
		"syscall.Exit inside init is one marker": {
			"package p\n\nimport \"syscall\"\n\nfunc init() {\n\tsyscall.Exit(1)\n}\n",
			[]Marker{{"exit-in-init", "p.go", 6}},
		},
		"os.Exit and syscall.Exit in package-level var initializers": {
			"package p\n\nimport (\n\t\"os\"\n\t\"syscall\"\n)\n\nvar a = func() int { os.Exit(1); return 0 }()\nvar (\n\tb = func() int { syscall.Exit(2); return 0 }()\n\tc = 3\n)\n",
			[]Marker{{"exit-in-init", "p.go", 8}, {"exit-in-init", "p.go", 10}},
		},
		"os.Exit in a function, and in a var of a function, is not a marker": {
			"package p\n\nimport \"os\"\n\nfunc f() {\n\tos.Exit(1)\n}\n\nfunc g() {\n\tvar x = func() { os.Exit(2) }\n\t_ = x\n}\n",
			nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Markers(map[string][]byte{"p.go": []byte(c.src)}); !slices.Equal(got, c.want) {
				t.Errorf("got %v\nwant %v", got, c.want)
			}
		})
	}
}

// go test marks the events of a test binary with a SYN byte (\x16). A package that prints such frames in an init and
// exits with status 0 reports whatever it likes and contradicts nothing: its literal is the only sign.
func TestMarkersFlagALiteralThatHoldsAFrameByte(t *testing.T) {
	// The source has the escapes as text; the last line is a raw string with the byte itself in it.
	const source = `package p

var a = "\x16=== RUN   TestX\n"
var b = "\026"
var c = "\u0016"
var e = "plain \x15 text"
var f = "\x1b[0m"
` + "var d = `\x16`\n"
	got := Markers(map[string][]byte{"p.go": []byte(source)})
	want := []Marker{{"forged-event", "p.go", 3}, {"forged-event", "p.go", 4}, {"forged-event", "p.go", 5}, {"forged-event", "p.go", 8}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}
