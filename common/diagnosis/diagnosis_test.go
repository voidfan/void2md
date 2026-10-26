package diagnosis

import (
	"errors"
	"testing"
)

func TestNewClientException(t *testing.T) {
	ex := NewClientException(ClientInvalidArgument, ErrorInvalidArgumentDiagnosisCode, "bad request")
	if ex.StatusCode != 400 {
		t.Errorf("expected StatusCode 400, got %d", ex.StatusCode)
	}
	if ex.Code != 400 {
		t.Errorf("expected Code 400, got %d", ex.Code)
	}
	if ex.DCode != ErrorInvalidArgumentDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Message != "bad request" {
		t.Errorf("unexpected Message: %s", ex.Message)
	}
	if ex.Err != nil {
		t.Errorf("expected nil Err, got %v", ex.Err)
	}
}

func TestNewServerException(t *testing.T) {
	ex := NewServerException(ServerInternal, ErrorUnknownDiagnosisCode, "internal error")
	if ex.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", ex.StatusCode)
	}
	if ex.Code != 500 {
		t.Errorf("expected Code 500, got %d", ex.Code)
	}
	if ex.DCode != ErrorUnknownDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
}

func TestNewAuthException(t *testing.T) {
	ex := NewAuthException("unauthorized")
	if ex.StatusCode != 401 {
		t.Errorf("expected StatusCode 401, got %d", ex.StatusCode)
	}
	if ex.Code != int(ClientUnauthenticated) {
		t.Errorf("expected Code 401, got %d", ex.Code)
	}
	if ex.DCode != ErrorAuthDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Err != nil {
		t.Errorf("expected nil Err, got %v", ex.Err)
	}
}

func TestNewParseException(t *testing.T) {
	parseErr := errors.New("parse failed")
	ex := NewParseException("pdf parse error", parseErr)
	if ex.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", ex.StatusCode)
	}
	if ex.Code != int(ServerInternal) {
		t.Errorf("expected Code 500, got %d", ex.Code)
	}
	if ex.DCode != ErrorPDFParseErrorDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Err != parseErr {
		t.Errorf("expected Err to be parseErr, got %v", ex.Err)
	}
	if ex.Message != "pdf parse error: parse failed" {
		t.Errorf("unexpected Message %q, want cause appended", ex.Message)
	}
}

func TestNewInvalidFileError_MessageIncludesCause(t *testing.T) {
	ex := NewInvalidFileError("invalid ofd", errors.New("OFD 文件缺少 OFD.xml"))
	if ex.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", ex.StatusCode)
	}
	if ex.DCode != ErrorInvalidFileDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Message != "invalid ofd: OFD 文件缺少 OFD.xml" {
		t.Errorf("unexpected Message %q, want cause appended", ex.Message)
	}
}

func TestNewOfficeConversionError_MessageIncludesCause(t *testing.T) {
	ex := NewOfficeConversionError("failed to convert doc to docx", errors.New("libreoffice: source file could not be loaded"))
	if ex.DCode != ErrorOfficeConversionDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Message != "failed to convert doc to docx: libreoffice: source file could not be loaded" {
		t.Errorf("unexpected Message %q, want cause appended", ex.Message)
	}
}

func TestNewOCRFailedError(t *testing.T) {
	ex := NewOCRFailedError("image parsing failed", errors.New("ocr timeout"))
	if ex.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", ex.StatusCode)
	}
	if ex.Code != int(ServerInternal) {
		t.Errorf("expected Code 500, got %d", ex.Code)
	}
	if ex.DCode != ErrorOCRFailedDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Message != "image parsing failed: ocr timeout" {
		t.Errorf("unexpected Message %q, want cause appended", ex.Message)
	}
	if ex.Err == nil {
		t.Error("expected non-nil Err")
	}
}

func TestNewServerException_MessageUnchanged(t *testing.T) {
	// 不带 err 的构造器不拼接 cause，message 保持原样
	ex := NewServerException(ServerInternal, ErrorUnknownDiagnosisCode, "internal error")
	if ex.Message != "internal error" {
		t.Errorf("unexpected Message %q, want unchanged", ex.Message)
	}
}

func TestNewAbortedException(t *testing.T) {
	ex := NewAbortedException("cancelled")
	if ex.StatusCode != 499 {
		t.Errorf("expected StatusCode 499, got %d", ex.StatusCode)
	}
	if ex.Code != int(ClientCancelled) {
		t.Errorf("expected Code 499, got %d", ex.Code)
	}
	if ex.DCode != ErrorAbortedDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Err != nil {
		t.Errorf("expected nil Err, got %v", ex.Err)
	}
}

func TestNewInvalidArgumentException(t *testing.T) {
	ex := NewInvalidArgumentException("missing field")
	if ex.StatusCode != 400 {
		t.Errorf("expected StatusCode 400, got %d", ex.StatusCode)
	}
	if ex.Code != int(ClientInvalidArgument) {
		t.Errorf("expected Code 400, got %d", ex.Code)
	}
	if ex.DCode != ErrorInvalidArgumentDiagnosisCode {
		t.Errorf("unexpected DCode: %s", ex.DCode)
	}
	if ex.Err != nil {
		t.Errorf("expected nil Err, got %v", ex.Err)
	}
}

func TestDiagnosisException_Error(t *testing.T) {
	ex := NewInvalidArgumentException("field X is required")
	if ex.Error() != "field X is required" {
		t.Errorf("expected 'field X is required', got '%s'", ex.Error())
	}
}

func TestDiagnosisException_Unwrap(t *testing.T) {
	// Parse exception carries underlying error
	parseErr := errors.New("line 42: unexpected token")
	ex := NewParseException("parse error", parseErr)
	if ex.Unwrap() != parseErr {
		t.Errorf("expected Unwrap to return parseErr")
	}

	// Other exceptions have nil Err
	ex2 := NewInvalidArgumentException("bad")
	if ex2.Unwrap() != nil {
		t.Errorf("expected Unwrap to return nil for non-parse exception, got %v", ex2.Unwrap())
	}
}

func TestDiagnosisException_ToDiagnosisError(t *testing.T) {
	ex := NewAuthException("auth failed")
	diag := ex.ToDiagnosisError()
	if diag.Code != "401" {
		t.Errorf("expected Code '401', got '%s'", diag.Code)
	}
	if diag.DiagnosisCode != ErrorAuthDiagnosisCode {
		t.Errorf("unexpected DiagnosisCode: %s", diag.DiagnosisCode)
	}
	if diag.Message != "auth failed" {
		t.Errorf("unexpected Message: %s", diag.Message)
	}
}

func TestToDiagnosisError_WithData(t *testing.T) {
	ex := &DiagnosisException{
		Code:       400,
		DCode:      ErrorInvalidArgumentDiagnosisCode,
		Message:    "validation failed",
		StatusCode: 400,
		Data:       map[string]string{"field": "name"},
	}
	diag := ex.ToDiagnosisError()
	if diag.Code != "400" {
		t.Errorf("expected Code '400', got '%s'", diag.Code)
	}
	if diag.Data == nil {
		t.Error("expected Data to be non-nil")
	}
}

func TestErrorCodeConstants(t *testing.T) {
	// Verify key constants are non-empty
	if ErrorInvalidArgumentDiagnosisCode == "" {
		t.Error("ErrorInvalidArgumentDiagnosisCode should not be empty")
	}
	if ErrorAuthDiagnosisCode == "" {
		t.Error("ErrorAuthDiagnosisCode should not be empty")
	}
	if ErrorUnknownDiagnosisCode == "" {
		t.Error("ErrorUnknownDiagnosisCode should not be empty")
	}
	if ErrorPDFParseErrorDiagnosisCode == "" {
		t.Error("ErrorPDFParseErrorDiagnosisCode should not be empty")
	}
	if ErrorAbortedDiagnosisCode == "" {
		t.Error("ErrorAbortedDiagnosisCode should not be empty")
	}
}

func TestClientErrorCode_Values(t *testing.T) {
	if int(ClientInvalidArgument) != 400 {
		t.Errorf("expected 400, got %d", ClientInvalidArgument)
	}
	if int(ClientUnauthenticated) != 401 {
		t.Errorf("expected 401, got %d", ClientUnauthenticated)
	}
	if int(ClientCancelled) != 499 {
		t.Errorf("expected 499, got %d", ClientCancelled)
	}
}

func TestServerErrorCode_Values(t *testing.T) {
	if int(ServerInternal) != 500 {
		t.Errorf("expected 500, got %d", ServerInternal)
	}
	if int(ServerUnavailable) != 503 {
		t.Errorf("expected 503, got %d", ServerUnavailable)
	}
}

// TestNewTooManyRequestsException 覆盖 /v1/htmlparse 存活任务超限时返回 429 的语义。
// 现有的 ClientResourceExhausted 只是状态码枚举，没有对应的 dcode 与构造函数，
// 因此这里断言新增的 FF000D。
func TestNewTooManyRequestsException(t *testing.T) {
	e := NewTooManyRequestsException("too many alive tasks")

	if e.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", e.StatusCode)
	}
	if e.Code != 429 {
		t.Errorf("Code = %d, want 429", e.Code)
	}
	if e.DCode != ErrorTooManyRequestsDiagnosisCode {
		t.Errorf("DCode = %q, want %q", e.DCode, ErrorTooManyRequestsDiagnosisCode)
	}
	if ErrorTooManyRequestsDiagnosisCode != "0x0000000000FF000D" {
		t.Errorf("dcode value = %q, want 0x0000000000FF000D", ErrorTooManyRequestsDiagnosisCode)
	}
	if e.Err != nil {
		t.Errorf("expected nil Err, got %v", e.Err)
	}

	// ToDiagnosisError 必须把 dcode 与 message 原样带出
	body := e.ToDiagnosisError()
	if body.Code != "429" {
		t.Errorf("response code = %q, want \"429\"", body.Code)
	}
	if body.DiagnosisCode != "0x0000000000FF000D" {
		t.Errorf("response diagnosisCode = %q", body.DiagnosisCode)
	}
	if body.Message != "too many alive tasks" {
		t.Errorf("response message = %q", body.Message)
	}
}

// TestDiagnosisCodes_Unique 确保所有内部诊断码互不重复。
// 注意不要退回「硬编码既有码列表 + 只比对新码」的形状：那种写法既被
// 「新码等于字面量 FF000D」的断言完全覆盖，又抓不到将来新增码的撞车。
func TestDiagnosisCodes_Unique(t *testing.T) {
	all := map[string]string{
		"ErrorWorkingDirDiagnosisCode":          ErrorWorkingDirDiagnosisCode,
		"ErrorDbMigrateDiagnosisCode":           ErrorDbMigrateDiagnosisCode,
		"ErrorDbInitialDiagnosisCode":           ErrorDbInitialDiagnosisCode,
		"ErrorParamCheckDiagnosisCode":          ErrorParamCheckDiagnosisCode,
		"ErrorUnknownDiagnosisCode":             ErrorUnknownDiagnosisCode,
		"ErrorAbortedDiagnosisCode":             ErrorAbortedDiagnosisCode,
		"ErrorOCRFailedDiagnosisCode":           ErrorOCRFailedDiagnosisCode,
		"ErrorPDFParseErrorDiagnosisCode":       ErrorPDFParseErrorDiagnosisCode,
		"ErrorUnsupportedFileTypeDiagnosisCode": ErrorUnsupportedFileTypeDiagnosisCode,
		"ErrorInvalidFileDiagnosisCode":         ErrorInvalidFileDiagnosisCode,
		"ErrorOfficeConversionDiagnosisCode":    ErrorOfficeConversionDiagnosisCode,
		"ErrorDocxNotValidDiagnosisCode":        ErrorDocxNotValidDiagnosisCode,
		"ErrorDocxConversionDiagnosisCode":      ErrorDocxConversionDiagnosisCode,
		"ErrorTooManyRequestsDiagnosisCode":     ErrorTooManyRequestsDiagnosisCode,
	}
	seen := make(map[string]string, len(all))
	for name, code := range all {
		if prev, dup := seen[code]; dup {
			t.Fatalf("诊断码重复: %s 与 %s 都是 %s", name, prev, code)
		}
		seen[code] = name
	}
}
