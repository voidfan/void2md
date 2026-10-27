package diagnosis

// External diagnosis codes
const (
	ErrorInvalidArgumentDiagnosisCode        = "0x0000000000000000"
	ErrorDbDiagnosisCode                     = "0x0000000000000000"
	ErrorAuthDiagnosisCode                   = "0x0000000000000001"
	ErrorNotFoundDiagnosisCode               = "0x0000000000000002"
	ErrorArgumentDiagnosisCode               = "0x0000000000000003"
	ErrorCaptchaDiagnosisCode                = "0x0000000000000004"
	ErrorKVCacheDiagnosisCode                = "0x0000000000000005"
	ErrorLogoutDiagnosisCode                 = "0x0000000000000006"
	ErrorKnowledgeBaseNotFoundCode           = "0x0000000000000000"
	ErrorDataNotFoundCode                    = "0x0000000000000000"
	ErrorAppNotFoundCode                     = "0x0000000000000000"
	ErrorDatasetNotFoundCode                 = "0x0000000000000000"
	ErrorServiceNotAvailableCode             = "0x0000000000000000"
	ErrorKnowledgeBaseDataCreateErrorCode    = "0x0000000000000000"
	ErrorKnowledgeBaseDatasetCreateErrorCode = "0x0000000000000000"
	ErrorFileExtractHeaderErrorCode          = "0x0000000000000000"

	// Internal diagnosis codes
	ErrorWorkingDirDiagnosisCode          = "0x0000000000FF0000"
	ErrorDbMigrateDiagnosisCode           = "0x0000000000FF0001"
	ErrorDbInitialDiagnosisCode           = "0x0000000000FF0002"
	ErrorParamCheckDiagnosisCode          = "0x0000000000FF0003"
	ErrorUnknownDiagnosisCode             = "0x0000000000FF0004"
	ErrorAbortedDiagnosisCode             = "0x0000000000FF0005"
	ErrorOCRFailedDiagnosisCode           = "0x0000000000FF0006"
	ErrorPDFParseErrorDiagnosisCode       = "0x0000000000FF0007"
	ErrorUnsupportedFileTypeDiagnosisCode = "0x0000000000FF0008"
	ErrorInvalidFileDiagnosisCode         = "0x0000000000FF0009"
	ErrorOfficeConversionDiagnosisCode    = "0x0000000000FF000A"
	ErrorDocxNotValidDiagnosisCode        = "0x0000000000FF000B"
	ErrorDocxConversionDiagnosisCode      = "0x0000000000FF000C"
	ErrorTooManyRequestsDiagnosisCode     = "0x0000000000FF000D"
)

// ClientErrorCode: 4xx client error codes
type ClientErrorCode int

const (
	ClientInvalidArgument     ClientErrorCode = 400
	ClientFailedPrecondition  ClientErrorCode = 400
	ClientOutOfRange          ClientErrorCode = 400
	ClientUnauthenticated     ClientErrorCode = 401
	ClientPermissionDenied    ClientErrorCode = 403
	ClientNotFound            ClientErrorCode = 404
	ClientAborted             ClientErrorCode = 409
	ClientAlreadyExists       ClientErrorCode = 409
	ClientUnprocessableEntity ClientErrorCode = 422
	ClientResourceExhausted   ClientErrorCode = 429
	ClientCancelled           ClientErrorCode = 499
	ClientCaptchaError        ClientErrorCode = 498
)

// ServerErrorCode: 5xx server error codes
type ServerErrorCode int

const (
	ServerDataLoss         ServerErrorCode = 500
	ServerUnknown          ServerErrorCode = 500
	ServerInternal         ServerErrorCode = 500
	ServerUnimplemented    ServerErrorCode = 501
	ServerBadGateway       ServerErrorCode = 502
	ServerUnavailable      ServerErrorCode = 503
	ServerDeadlineExceeded ServerErrorCode = 504
)

// DiagnosisError is the standard error response body.
type DiagnosisError struct {
	Code          string      `json:"code"`
	DiagnosisCode string      `json:"diagnosisCode"`
	Message       string      `json:"message"`
	Data          interface{} `json:"data,omitempty"`
}

// DiagnosisException carries the full error context.
type DiagnosisException struct {
	Code       int
	DCode      string
	Message    string
	StatusCode int
	Err        error
	Data       interface{}
}

// Error implements the error interface.
func (e *DiagnosisException) Error() string {
	return e.Message
}

// Unwrap returns the underlying error.
func (e *DiagnosisException) Unwrap() error {
	return e.Err
}

// NewClientException creates a client exception.
func NewClientException(code ClientErrorCode, dcode string, message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(code),
		DCode:      dcode,
		Message:    message,
		StatusCode: int(code),
	}
}

// NewServerException creates a server exception.
func NewServerException(code ServerErrorCode, dcode string, message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(code),
		DCode:      dcode,
		Message:    message,
		StatusCode: int(code),
	}
}

// NewAuthException creates an auth exception.
func NewAuthException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientUnauthenticated),
		DCode:      ErrorAuthDiagnosisCode,
		Message:    message,
		StatusCode: 401,
	}
}

// NewParseException creates a parse exception.
func NewParseException(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorPDFParseErrorDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewOCRFailedError creates an OCR/image-recognition failure.
func NewOCRFailedError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorOCRFailedDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewUnsupportedFileTypeError creates an unsupported-file-type error.
func NewUnsupportedFileTypeError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorUnsupportedFileTypeDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewInvalidFileError creates an invalid-file error.
func NewInvalidFileError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorInvalidFileDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewOfficeConversionError creates an Office-conversion error.
func NewOfficeConversionError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorOfficeConversionDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewDocxNotValidError creates a DOCX-invalid error.
func NewDocxNotValidError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorDocxNotValidDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewDocxConversionError creates a DOCX-conversion error.
func NewDocxConversionError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorDocxConversionDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewAbortedException creates an aborted/canceled exception.
func NewAbortedException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientCancelled),
		DCode:      ErrorAbortedDiagnosisCode,
		Message:    message,
		StatusCode: 499,
	}
}

// NewInvalidArgumentException creates an invalid-argument exception.
func NewInvalidArgumentException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientInvalidArgument),
		DCode:      ErrorInvalidArgumentDiagnosisCode,
		Message:    message,
		StatusCode: 400,
	}
}

// NewTooManyRequestsException creates a too-many-requests exception (HTTP 429).
func NewTooManyRequestsException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientResourceExhausted),
		DCode:      ErrorTooManyRequestsDiagnosisCode,
		Message:    message,
		StatusCode: 429,
	}
}

// ToDiagnosisError converts a DiagnosisException into a DiagnosisError response body.
func (e *DiagnosisException) ToDiagnosisError() DiagnosisError {
	return DiagnosisError{
		Code:          intToStr(e.Code),
		DiagnosisCode: e.DCode,
		Message:       e.Message,
		Data:          e.Data,
	}
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte(i%10) + '0'
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// causeText returns the cause suffix appended to the error Message so API responses
// expose the root failure reason; empty when err is nil.
func causeText(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
