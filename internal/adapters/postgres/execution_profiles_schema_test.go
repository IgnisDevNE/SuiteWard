//go:build integration

package postgres_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// Migration 00023 (docs/contracts/persistence.md, "Migration 00023: several profiles (M2.2; normative)"): a job's profile is a label of one or
// several ids, a missing image is a reason of the family image-missing-<id>, and a complete result keeps its parts and the tests a receipt removed.
// Each refused row breaks one constraint: PostgreSQL checks them in the order of their names.

// profileSeed is a database at the supported version with a command entry and one pending job, which has the id 1 that the default rows name.
func profileSeed(t *testing.T) schemaDatabase {
	t.Helper()
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	return database
}

// endJob moves the seed job to leased, as a lease does, and then to the state and reason of the statement given (an UPDATE of the job).
func endJob(t *testing.T, tx pgx.Tx, set string) error {
	t.Helper()
	schemaExec(t, tx, "UPDATE execution_jobs SET state = 'leased', attempts = 1, lease_id = '"+execLease(1)+"', leased_at = '"+execAt+
		"', lease_expires_at = '2026-10-26T12:01:00Z', deadline_at = '2026-10-26T12:10:00Z'")
	_, err := tx.Exec(t.Context(), "UPDATE execution_jobs SET "+set+", lease_expires_at = NULL, deadline_at = NULL, finished_at = '"+execAt+"'")
	return err
}

func TestSchemaJobsAcceptAProfileLabel(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	segment16, segment15 := strings.Repeat("a", 16), strings.Repeat("b", 15)
	for _, profile := range []string{"go", "cpp", "cpp-go", "a", "a1-b2-c3", segment16, segment16 + "-" + segment15} { // the last is 32 bytes
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if err := insertRow(t.Context(), tx, "execution_jobs", map[string]any{"profile": profile}); err != nil {
				t.Errorf("a job with the profile %q is refused: %v", profile, err)
			}
		})
	}
	for _, test := range []struct{ name, profile string }{
		{"an empty label", ""},
		{"an upper-case label", "Cpp"},
		{"an upper-case id", "cpp-Go"},
		{"a repeated hyphen", "cpp--go"},
		{"a leading hyphen", "-cpp"},
		{"a trailing hyphen", "cpp-"},
		{"a hyphen alone", "-"},
		{"an id that starts with a digit", "1cpp"},
		{"an id that starts with a digit after a hyphen", "cpp-1go"},
		{"an underscore", "cpp_go"},
		{"a space", "cpp go"},
		{"a segment of 17 bytes", strings.Repeat("a", 17)},
		{"a second segment of 17 bytes", "go-" + strings.Repeat("a", 17)},
		{"a label of 33 bytes", segment16 + "-" + segment16},
		{"three segments of 16 bytes", segment16 + "-" + segment16 + "-" + segment16},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_jobs", map[string]any{"profile": test.profile}), "23514", "execution_jobs_profile_check")
			})
		})
	}
}

func TestSchemaImageMissingOnlyWhenUnavailable(t *testing.T) {
	database := profileSeed(t)
	const reasonCheck = "execution_jobs_reason_check"
	malformed := []string{"image-missing-", "image-missing-Cpp", "image-missing-c-pp", "image-missing-1cpp", "image-missing-" + strings.Repeat("a", 17), "image-missing", "image-missing-cpp-go"}

	// A job that is unavailable holds the family, and every reason of 00021 that it held before.
	for _, reason := range []string{"image-missing-cpp", "image-missing-a", "image-missing-" + strings.Repeat("a", 16), "executor-error", "sandbox-unavailable", "archive-unavailable", "lease-expired",
		"timeout", "github-unavailable", "no-executor", "fetch-failed", "install-failed", "resource-limit", "toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported",
		"executor-not-configured", "no-merge-base"} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if err := endJob(t, tx, "state = 'unavailable', reason = '"+reason+"'"); err != nil {
				t.Errorf("an unavailable job with the reason %q is refused: %v", reason, err)
			}
		})
	}
	for _, reason := range malformed {
		t.Run("an unavailable job with "+reason, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, endJob(t, tx, "state = 'unavailable', reason = '"+reason+"'"), "23514", reasonCheck)
			})
		})
	}
	// An open job never holds it, because it is not retried, and a cancelled job has its own reasons.
	for name, statement := range map[string]string{
		"a pending job": "UPDATE execution_jobs SET reason = 'image-missing-cpp'",
		"a leased job": "UPDATE execution_jobs SET state = 'leased', reason = 'image-missing-cpp', attempts = 1, lease_id = '" + execLease(1) + "', leased_at = '" + execAt +
			"', lease_expires_at = '2026-10-26T12:01:00Z', deadline_at = '2026-10-26T12:10:00Z'",
		"a cancelled job": "UPDATE execution_jobs SET state = 'cancelled', reason = 'image-missing-cpp', finished_at = '" + execAt + "'",
	} {
		t.Run(name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				_, err := tx.Exec(t.Context(), statement)
				schemaRequireError(t, err, "23514", reasonCheck)
			})
		})
	}
	// The reasons of the open and the cancelled job do not change.
	for _, reason := range []string{"executor-error", "sandbox-unavailable", "archive-unavailable", "lease-expired", "timeout", "fetch-failed", "github-unavailable"} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if _, err := tx.Exec(t.Context(), "UPDATE execution_jobs SET reason = '"+reason+"'"); err != nil {
				t.Errorf("a pending job with the reason %q is refused: %v", reason, err)
			}
		})
	}
	for _, reason := range []string{"head-moved", "closed", "obsolete"} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if _, err := tx.Exec(t.Context(), "UPDATE execution_jobs SET state = 'cancelled', reason = '"+reason+"', finished_at = '"+execAt+"'"); err != nil {
				t.Errorf("a cancelled job with the reason %q is refused: %v", reason, err)
			}
		})
	}

	// The same family is a reason of an unavailable result, next to the reasons of the executor, and a complete result refuses it.
	unavailable := map[string]any{"status": "unavailable", "image_digest": nil, "toolchain": nil, "runtime": nil}
	with := func(reason any) map[string]any {
		row := map[string]any{"reason": reason}
		for column, value := range unavailable {
			row[column] = value
		}
		return row
	}
	for _, reason := range []string{"image-missing-cpp", "image-missing-" + strings.Repeat("a", 16), "executor-error", "sandbox-unavailable", "archive-unavailable", "github-unavailable", "fetch-failed",
		"install-failed", "resource-limit", "toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported"} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if err := insertRow(t.Context(), tx, "execution_results", with(reason)); err != nil {
				t.Errorf("an unavailable result with the reason %q is refused: %v", reason, err)
			}
		})
	}
	const resultReasonCheck = "execution_results_reason_check"
	for _, reason := range malformed {
		t.Run("an unavailable result with "+reason, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", with(reason)), "23514", resultReasonCheck)
			})
		})
	}
	t.Run("a complete result with the family", func(t *testing.T) {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", map[string]any{"reason": "image-missing-cpp"}), "23514", resultReasonCheck)
		})
	})
}

// jsonList is a JSON array of n copies of entry.
func jsonList(n int, entry string) string {
	return "[" + strings.TrimSuffix(strings.Repeat(entry+",", n), ",") + "]"
}

// jsonObject is a JSON object of the fields of base with those of change set, and with a field left out when its value in change is nil.
func jsonObject(t *testing.T, base, change map[string]any) string {
	t.Helper()
	fields := map[string]any{}
	for name, value := range base {
		fields[name] = value
	}
	for name, value := range change {
		if value == nil {
			delete(fields, name)
		} else {
			fields[name] = value
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// jsonCase is a value of a jsonb column of the default complete result.
type jsonCase struct{ name, value string }

// requireColumn inserts the default complete result with each value in a column in a transaction that is rolled back: the refused values break
// the constraint named, and the accepted ones are stored.
func requireColumn(t *testing.T, database schemaDatabase, column, constraint string, refused, accepted []jsonCase) {
	t.Helper()
	for _, test := range refused {
		t.Run("refuses "+test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", map[string]any{column: test.value}), "23514", constraint)
			})
		})
	}
	for _, test := range accepted {
		t.Run("accepts "+test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				if err := insertRow(t.Context(), tx, "execution_results", map[string]any{column: test.value}); err != nil {
					t.Errorf("%s is refused: %v", test.name, err)
				}
			})
		})
	}
}

// entryCases are the values of a list whose entries are objects of base with the string fields named, each field with the bounds in bytes given. The refused
// ones put a malformed entry after a good one, so that every entry is checked.
func entryCases(t *testing.T, base map[string]any, bounds map[string][2]int, most int) (refused, accepted []jsonCase) {
	t.Helper()
	good := jsonObject(t, base, nil)
	after := func(entry string) string { return "[" + good + "," + entry + "]" }
	refused = []jsonCase{
		{"an object", `{}`}, {"a string", `"x"`}, {"a number", `1`}, {"null", `null`},
		{"too many entries", jsonList(most+1, good)},
		{"a number as an entry", after(`1`)}, {"a string as an entry", after(`"x"`)}, {"an array as an entry", after(`[]`)}, {"null as an entry", after(`null`)},
		{"an empty object as an entry", after(`{}`)},
	}
	accepted = []jsonCase{{"no entry", `[]`}, {"one entry", "[" + good + "]"}, {"the most entries", jsonList(most, good)},
		{"an entry with another key", "[" + jsonObject(t, base, map[string]any{"extra": true}) + "]"}}
	for field, bound := range bounds {
		refused = append(refused,
			jsonCase{"an entry without " + field, after(jsonObject(t, base, map[string]any{field: nil}))},
			jsonCase{field + " as a number", after(jsonObject(t, base, map[string]any{field: 7}))},
			jsonCase{field + " as null", after(jsonObject(t, base, map[string]any{field: json.RawMessage("null")}))},
			jsonCase{field + " as an array", after(jsonObject(t, base, map[string]any{field: []string{"x"}}))})
		if bound[0] > 0 { // a field that must not be empty
			refused = append(refused, jsonCase{"an empty " + field, after(jsonObject(t, base, map[string]any{field: ""}))})
		}
		if bound[1] > 0 {
			refused = append(refused, jsonCase{field + " over its bound", after(jsonObject(t, base, map[string]any{field: strings.Repeat("a", bound[1]+1)}))})
			accepted = append(accepted, jsonCase{field + " of the longest size", "[" + jsonObject(t, base, map[string]any{field: strings.Repeat("a", bound[1])}) + "]"})
			if field != "profile" && field != "imageDigest" { // the others take any text: the bound counts bytes
				refused = append(refused, jsonCase{field + " of more bytes than characters", after(jsonObject(t, base, map[string]any{field: strings.Repeat("é", bound[1]/2+1)}))})
				accepted = append(accepted, jsonCase{field + " of the longest size in two-byte characters", "[" + jsonObject(t, base, map[string]any{field: strings.Repeat("é", bound[1]/2)}) + "]"})
			}
		}
	}
	return refused, accepted
}

var partBase = map[string]any{"profile": "go", "folder": ".", "imageDigest": schemaDigest, "toolchain": "go1.25.1"}

func TestSchemaResultsBoundParts(t *testing.T) {
	database := profileSeed(t)
	// profile and imageDigest have a form and not a bound of their own: profile is at most 16 bytes, and the digest is exact.
	refused, accepted := entryCases(t, partBase, map[string][2]int{"profile": {1, 16}, "folder": {1, 512}, "imageDigest": {1, 0}, "toolchain": {1, 64}}, 8)
	for _, profile := range []string{"Go", "1go", "go-x", "g o", "go\n"} {
		refused = append(refused, jsonCase{"the profile " + profile, "[" + jsonObject(t, partBase, map[string]any{"profile": profile}) + "]"})
	}
	for _, digest := range []string{"sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("a", 65), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64),
		"md5:" + strings.Repeat("a", 64), strings.Repeat("a", 64), schemaDigest + "\n"} {
		refused = append(refused, jsonCase{"the image digest " + digest, "[" + jsonObject(t, partBase, map[string]any{"imageDigest": digest}) + "]"})
	}
	requireColumn(t, database, "parts", "execution_results_parts_check", refused, accepted)
}

func TestSchemaResultsPartsOnlyWhenComplete(t *testing.T) {
	database := profileSeed(t)
	unavailable := map[string]any{"status": "unavailable", "reason": "executor-error", "image_digest": nil, "toolchain": nil, "runtime": nil}
	with := func(parts string) map[string]any {
		row := map[string]any{"parts": parts}
		for column, value := range unavailable {
			row[column] = value
		}
		return row
	}
	inRolledBackTransaction(t, database, func(tx pgx.Tx) {
		schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", with("["+jsonObject(t, partBase, nil)+"]")), "23514", "execution_results_parts_complete_check")
	})
	inRolledBackTransaction(t, database, func(tx pgx.Tx) {
		if err := insertRow(t.Context(), tx, "execution_results", with("[]")); err != nil {
			t.Errorf("an unavailable result with no parts is refused: %v", err)
		}
	})
	inRolledBackTransaction(t, database, func(tx pgx.Tx) {
		if err := insertRow(t.Context(), tx, "execution_results", map[string]any{"parts": "[" + jsonObject(t, partBase, nil) + "]"}); err != nil {
			t.Errorf("a complete result with a part is refused: %v", err)
		}
	})
}

var removedBase = map[string]any{"package": "pkg", "name": "TestA", "file": "pkg/a_test.go"}

func TestSchemaResultsBoundRemoved(t *testing.T) {
	database := profileSeed(t)
	refused, accepted := entryCases(t, removedBase, map[string][2]int{"package": {1, 512}, "name": {1, 200}, "file": {1, 1024}}, 1000)
	requireColumn(t, database, "removed", "execution_results_removed_check", refused, accepted)

	// A result that is not complete removed nothing.
	unavailable := map[string]any{"status": "unavailable", "reason": "executor-error", "image_digest": nil, "toolchain": nil, "runtime": nil, "removed": "[" + jsonObject(t, removedBase, nil) + "]"}
	inRolledBackTransaction(t, database, func(tx pgx.Tx) {
		schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", unavailable), "23514", "execution_results_removed_complete_check")
	})
	unavailable["removed"] = "[]"
	inRolledBackTransaction(t, database, func(tx pgx.Tx) {
		if err := insertRow(t.Context(), tx, "execution_results", unavailable); err != nil {
			t.Errorf("an unavailable result with no removed test is refused: %v", err)
		}
	})
}

// A database at version 22 keeps its job and its result: both rows read as they did, and the new columns read as empty lists. execution_results is insert-only,
// and a migration is no DML, so the triggers that reject an UPDATE do not stand in the way of its two new columns.
func TestMigrateTo23KeepsRowsOf22(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 22); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	mustInsert(t, database.conn, "execution_results", nil)
	mustInsert(t, database.conn, "execution_tests", nil)
	var job, result, test string
	if err := database.conn.QueryRow(t.Context(), `SELECT (SELECT to_jsonb(j)::text FROM execution_jobs j), (SELECT to_jsonb(r)::text FROM execution_results r),
		(SELECT to_jsonb(t)::text FROM execution_tests t)`).Scan(&job, &result, &test); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var jobKept, resultKept, testKept, parts, removed bool
	if err := database.conn.QueryRow(t.Context(), `SELECT (SELECT to_jsonb(j) = $1::jsonb FROM execution_jobs j),
		(SELECT to_jsonb(r) - 'parts' - 'removed' = $2::jsonb FROM execution_results r), (SELECT to_jsonb(t) = $3::jsonb FROM execution_tests t),
		(SELECT parts = '[]'::jsonb FROM execution_results), (SELECT removed = '[]'::jsonb FROM execution_results)`, job, result, test).Scan(&jobKept, &resultKept, &testKept, &parts, &removed); err != nil {
		t.Fatal(err)
	}
	if !jobKept || !resultKept || !testKept || !parts || !removed {
		t.Fatalf("after the migration: job kept %v, result kept %v, test kept %v, parts empty %v, removed empty %v; want all true", jobKept, resultKept, testKept, parts, removed)
	}
	for _, column := range []string{"parts", "removed"} {
		var nullable, defaultsEmpty bool
		var dataType string
		if err := database.conn.QueryRow(t.Context(), `SELECT is_nullable = 'YES', column_default = '''[]''::jsonb', data_type FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = 'execution_results' AND column_name = $2`, database.schema, column).Scan(&nullable, &defaultsEmpty, &dataType); err != nil || nullable || !defaultsEmpty || dataType != "jsonb" {
			t.Fatalf("execution_results.%s: nullable %v, default [] %v, type %q, error %v; want jsonb NOT NULL DEFAULT '[]'", column, nullable, defaultsEmpty, dataType, err)
		}
	}
	// The row is still insert-only.
	if _, err := database.conn.Exec(t.Context(), "UPDATE execution_results SET parts = '[]'"); err == nil {
		t.Fatal("a result was updated after the migration")
	}
}
