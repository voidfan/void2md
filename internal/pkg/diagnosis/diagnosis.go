package diagnosis

// 外部排障码定义
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

	// 内部排障码定义
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

// ClientErrorCode 客户端错误码 400-499
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

// ServerErrorCode 服务端错误码 500-599
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

// DiagnosisError 标准错误响应体
type DiagnosisError struct {
	Code          string      `json:"code"`
	DiagnosisCode string      `json:"diagnosisCode"`
	Message       string      `json:"message"`
	Data          interface{} `json:"data,omitempty"`
}

// DiagnosisException 诊断异常，携带完整错误上下文
type DiagnosisException struct {
	Code       int
	DCode      string
	Message    string
	StatusCode int
	Err        error
	Data       interface{}
}

// Error 实现 error 接口
func (e *DiagnosisException) Error() string {
	return e.Message
}

// Unwrap 返回底层的 error
func (e *DiagnosisException) Unwrap() error {
	return e.Err
}

// NewClientException 创建客户端异常
func NewClientException(code ClientErrorCode, dcode string, message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(code),
		DCode:      dcode,
		Message:    message,
		StatusCode: int(code),
	}
}

// NewServerException 创建服务端异常
func NewServerException(code ServerErrorCode, dcode string, message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(code),
		DCode:      dcode,
		Message:    message,
		StatusCode: int(code),
	}
}

// NewAuthException 创建认证异常
func NewAuthException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientUnauthenticated),
		DCode:      ErrorAuthDiagnosisCode,
		Message:    message,
		StatusCode: 401,
	}
}

// NewParseException 创建解析异常
func NewParseException(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorPDFParseErrorDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewOCRFailedError 创建 OCR/图片识别失败异常
func NewOCRFailedError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorOCRFailedDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewUnsupportedFileTypeError 创建不支持的文件类型异常
func NewUnsupportedFileTypeError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorUnsupportedFileTypeDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewInvalidFileError 创建无效文件异常
func NewInvalidFileError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorInvalidFileDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewOfficeConversionError 创建 Office 转换异常
func NewOfficeConversionError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorOfficeConversionDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewDocxNotValidError 创建 DOCX 无效异常
func NewDocxNotValidError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorDocxNotValidDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewDocxConversionError 创建 DOCX 转换异常
func NewDocxConversionError(message string, err error) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ServerInternal),
		DCode:      ErrorDocxConversionDiagnosisCode,
		Message:    message + causeText(err),
		StatusCode: 500,
		Err:        err,
	}
}

// NewAbortedException 创建取消异常
func NewAbortedException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientCancelled),
		DCode:      ErrorAbortedDiagnosisCode,
		Message:    message,
		StatusCode: 499,
	}
}

// NewInvalidArgumentException 创建无效参数异常
func NewInvalidArgumentException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientInvalidArgument),
		DCode:      ErrorInvalidArgumentDiagnosisCode,
		Message:    message,
		StatusCode: 400,
	}
}

// NewTooManyRequestsException 创建请求过多异常（HTTP 429）
func NewTooManyRequestsException(message string) *DiagnosisException {
	return &DiagnosisException{
		Code:       int(ClientResourceExhausted),
		DCode:      ErrorTooManyRequestsDiagnosisCode,
		Message:    message,
		StatusCode: 429,
	}
}

// ToDiagnosisError 将 DiagnosisException 转换为 DiagnosisError 响应体
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

// causeText 返回底层错误原因的后缀文本，用于把具体原因拼进错误 Message，
// 使接口响应能直接向调用方暴露解析失败的根本原因。err 为 nil 时返回空串。
func causeText(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
