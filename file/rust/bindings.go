package rust

/*
#include <stdlib.h>

// Forward declarations for Rust FFI functions
typedef struct {
    char* markdown;
    char* title;
    int success;
} CStringResult;

CStringResult void2md_convert_buffer(
    const unsigned char* input,
    size_t input_len,
    const char* mimetype,
    const char* extension,
    const char* filename,
    const char* image_dir,
    int embed_images
);

CStringResult void2md_convert_buffer_with_config(
    const unsigned char* input,
    size_t input_len,
    const char* mimetype,
    const char* extension,
    const char* filename,
    const char* image_dir,
    int embed_images,
    const char* config_json
);

CStringResult void2md_convert_file(
    const char* file_path,
    const char* mimetype,
    const char* extension,
    const char* filename,
    const char* image_dir,
    int embed_images
);

void void2md_free_string(char* ptr);

char* void2md_converter_names();
*/
import "C"
import (
	"unsafe"
)

// ConversionResult represents the result from Rust conversion
type ConversionResult struct {
	Markdown string
	Title    string
	Success  bool
}

// ConvertBufferToMarkdown converts a byte buffer to markdown using Rust
func ConvertBufferToMarkdown(buffer []byte, mimetype, extension, filename string, embedImages bool, configJSON string) (*ConversionResult, error) {
	// Convert Go strings to C strings
	var cMimetype *C.char
	if mimetype != "" {
		cMimetype = C.CString(mimetype)
		defer C.free(unsafe.Pointer(cMimetype))
	}

	var cExtension *C.char
	if extension != "" {
		cExtension = C.CString(extension)
		defer C.free(unsafe.Pointer(cExtension))
	}

	var cFilename *C.char
	if filename != "" {
		cFilename = C.CString(filename)
		defer C.free(unsafe.Pointer(cFilename))
	}

	var cImageDir *C.char // Always null when embedding images

	var cConfigJSON *C.char
	if configJSON != "" {
		cConfigJSON = C.CString(configJSON)
		defer C.free(unsafe.Pointer(cConfigJSON))
	}

	// Call Rust function with embed_images parameter
	result := C.void2md_convert_buffer_with_config(
		(*C.uchar)(&buffer[0]),
		C.size_t(len(buffer)),
		cMimetype,
		cExtension,
		cFilename,
		cImageDir,
		C.int(boolToInt(embedImages)),
		cConfigJSON,
	)

	// Handle result
	if result.success == 0 {
		// Error case - markdown field contains error message
		errorMsg := C.GoString(result.markdown)
		if result.markdown != nil {
			C.void2md_free_string(result.markdown)
		}
		return nil, &RustError{errorMsg}
	}

	// Success case
	markdown := C.GoString(result.markdown)
	title := ""
	if result.title != nil {
		title = C.GoString(result.title)
	}

	// Free allocated strings
	if result.markdown != nil {
		C.void2md_free_string(result.markdown)
	}
	if result.title != nil {
		C.void2md_free_string(result.title)
	}

	return &ConversionResult{
		Markdown: markdown,
		Title:    title,
		Success:  true,
	}, nil
}

// convertFileToMarkdown converts a file to markdown using Rust
func ConvertFileToMarkdown(filePath, mimetype, extension, filename string, embedImages bool) (*ConversionResult, error) {
	// Convert Go strings to C strings
	cFilePath := C.CString(filePath)
	defer C.free(unsafe.Pointer(cFilePath))

	var cMimetype *C.char
	if mimetype != "" {
		cMimetype = C.CString(mimetype)
		defer C.free(unsafe.Pointer(cMimetype))
	}

	var cExtension *C.char
	if extension != "" {
		cExtension = C.CString(extension)
		defer C.free(unsafe.Pointer(cExtension))
	}

	var cFilename *C.char
	if filename != "" {
		cFilename = C.CString(filename)
		defer C.free(unsafe.Pointer(cFilename))
	}

	var cImageDir *C.char // Always null when embedding images

	// Call Rust function with embed_images parameter
	result := C.void2md_convert_file(
		cFilePath,
		cMimetype,
		cExtension,
		cFilename,
		cImageDir,
		C.int(boolToInt(embedImages)),
	)

	// Handle result
	if result.success == 0 {
		// Error case - markdown field contains error message
		errorMsg := C.GoString(result.markdown)
		if result.markdown != nil {
			C.void2md_free_string(result.markdown)
		}
		return nil, &RustError{errorMsg}
	}

	// Success case
	markdown := C.GoString(result.markdown)
	title := ""
	if result.title != nil {
		title = C.GoString(result.title)
	}

	// Free allocated strings
	if result.markdown != nil {
		C.void2md_free_string(result.markdown)
	}
	if result.title != nil {
		C.void2md_free_string(result.title)
	}

	return &ConversionResult{
		Markdown: markdown,
		Title:    title,
		Success:  true,
	}, nil
}

// GetConverterNames returns a comma-separated list of supported converter names
func GetConverterNames() (string, error) {
	cNames := C.void2md_converter_names()
	if cNames == nil {
		return "", &RustError{"Failed to get converter names"}
	}
	names := C.GoString(cNames)
	C.void2md_free_string(cNames)
	return names, nil
}

// RustError represents an error from the Rust library
type RustError struct {
	Message string
}

func (e *RustError) Error() string {
	return e.Message
}

// boolToInt converts bool to int (1 for true, 0 for false)
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}