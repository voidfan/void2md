package metadata

import (
	"strconv"
)

// SQLError corresponds to Python's SQLError(StrEnum); values are lowercase member names.
type SQLError string

const (
	SQLErrorEmptySQL            SQLError = "emptysql"
	SQLErrorSyntaxError         SQLError = "syntaxerror"
	SQLErrorNotAllowedOperation SQLError = "notallowedoperation"
	SQLErrorDBError             SQLError = "dberror"
	SQLErrorUnknownError        SQLError = "unknownerror"
)

// SQLException corresponds to Python's SQLException.
type SQLException struct {
	ErrorType SQLError `json:"error_type"`
	Msg       string   `json:"msg"`
	Code      int      `json:"code"`
}

// Error makes SQLException usable via the error interface.
func (e *SQLException) Error() string { return e.Msg }

// DBException corresponds to Python's DBException.
type DBException struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// DBServerVersion corresponds to Python's DBServerVersion. Patch is a pointer:
// Python defaults to None, serialized as null.
type DBServerVersion struct {
	Major int  `json:"major"`
	Minor int  `json:"minor"`
	Patch *int `json:"patch"`
}

// String: __str__ emits only "major.minor" when patch is None.
func (v DBServerVersion) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
	if v.Patch == nil {
		return s
	}
	return s + "." + strconv.Itoa(*v.Patch)
}
