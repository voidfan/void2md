package api

import (
	"errors"
	"strings"
	"testing"

	"void2md/structured/pkg/ds"
)

// TestExcelUploadWhitelistIsIngestable: the whitelist may be narrower than the engine's
// capability but never broader (regression: xls was whitelisted with no Go reader).
func TestExcelUploadWhitelistIsIngestable(t *testing.T) {
	if len(excelSupportedFileExtensions) == 0 {
		t.Fatal("excelSupportedFileExtensions must not be empty")
	}
	for ext := range excelSupportedFileExtensions {
		if !ds.IngestableExtension(ext) {
			t.Errorf("%s is in the upload whitelist but the engine cannot read it: add a reader implementation or remove it from the whitelist", ext)
		}
	}
	if excelSupportedFileExtensions["xls"] {
		t.Error("xls has no reader implementation on the Go side (DuckDB only supports xlsx/xlsm) and must not appear in the upload whitelist")
	}
	for _, ext := range []string{"xlsx", "csv"} {
		if !excelSupportedFileExtensions[ext] {
			t.Errorf("the upload whitelist should contain %s", ext)
		}
	}
}

// TestNewExcelStoreRejectsUnsupportedType locks the dedicated unsupported-type error path
// (regression: non-s3 types used to fall into mongo, masking the real cause). Only types that
// initiate no I/O are tested; s3/mongo construct connections.
func TestNewExcelStoreRejectsUnsupportedType(t *testing.T) {
	for _, typ := range []string{"local", "duckdb", "mysql", "s3x"} {
		_, err := newExcelStore(`{"type":"` + typ + `"}`)
		var unsupported *unsupportedExcelStoreTypeError
		if !errors.As(err, &unsupported) {
			t.Fatalf("type=%s: expected unsupportedExcelStoreTypeError, got %v", typ, err)
		}
		if unsupported.Type != typ {
			t.Errorf("type=%s: the Type recorded in the error is %q", typ, unsupported.Type)
		}
	}

	// Invalid JSON must take Python's "invalid configuration information" path, not unsupported-type.
	_, err := newExcelStore("not json")
	if err == nil {
		t.Fatal("invalid JSON should produce an error")
	}
	var unsupported *unsupportedExcelStoreTypeError
	if errors.As(err, &unsupported) {
		t.Errorf("invalid JSON should not be classified as \"unsupported type\": %v", err)
	}
}

// TestUnsupportedExcelStoreTypeErrorMessage: the real cause must be visible in the logged
// message (ops rely on that single log entry).
func TestUnsupportedExcelStoreTypeErrorMessage(t *testing.T) {
	msg := (&unsupportedExcelStoreTypeError{Type: "local"}).Error()
	for _, want := range []string{"local", "s3", "mongo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error message should contain %q: %s", want, msg)
		}
	}
}
