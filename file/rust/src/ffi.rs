use std::ffi::{CStr, CString};
use std::os::raw::{c_char, c_int};

use crate::void2md::Void2md;
use crate::types::StreamInfo;

/// C-compatible string structure for returning conversion results
#[repr(C)]
pub struct CStringResult {
    pub markdown: *mut c_char,
    pub title: *mut c_char,
    pub success: bool,
}

impl CStringResult {
    fn new(markdown: String, title: Option<String>) -> Self {
        let markdown_cstr = CString::new(markdown).unwrap();
        let title_cstr = match title.as_ref() {
            Some(t) => CString::new(t.as_str()).unwrap(),
            None => CString::new("").unwrap(),
        };

        CStringResult {
            markdown: markdown_cstr.into_raw(),
            title: if title.is_some() { title_cstr.into_raw() } else { std::ptr::null_mut() },
            success: true,
        }
    }

    fn error(msg: &str) -> Self {
        let msg_cstr = CString::new(msg).unwrap();
        CStringResult {
            markdown: msg_cstr.into_raw(),
            title: std::ptr::null_mut(),
            success: false,
        }
    }
}

/// Convert a buffer to markdown with configuration
///
/// # Arguments
/// * `input` - Pointer to input buffer
/// * `input_len` - Length of input buffer
/// * `mimetype` - Optional mimetype (can be null)
/// * `extension` - Optional file extension (can be null)
/// * `filename` - Optional filename (can be null)
/// * `image_dir` - Optional image directory (can be null)
/// * `embed_images` - Whether to embed images as base64 data URLs (1 = true, 0 = false)
/// * `config_json` - Optional configuration JSON string (can be null)
///
/// # Returns
/// * `CStringResult` - Contains markdown string, optional title, and success flag
#[no_mangle]
pub extern "C" fn void2md_convert_buffer_with_config(
    input: *const u8,
    input_len: usize,
    mimetype: *const c_char,
    extension: *const c_char,
    filename: *const c_char,
    image_dir: *const c_char,
    embed_images: c_int,
    config_json: *const c_char,
) -> CStringResult {
    // Safety: We assume the caller provides valid pointers
    let buffer = unsafe { std::slice::from_raw_parts(input, input_len) };

    let mimetype_str = if !mimetype.is_null() {
        Some(unsafe { CStr::from_ptr(mimetype).to_string_lossy().into_owned() })
    } else {
        None
    };

    let extension_str = if !extension.is_null() {
        Some(unsafe { CStr::from_ptr(extension).to_string_lossy().into_owned() })
    } else {
        None
    };

    let filename_str = if !filename.is_null() {
        Some(unsafe { CStr::from_ptr(filename).to_string_lossy().into_owned() })
    } else {
        None
    };

    let image_dir_str = if !image_dir.is_null() {
        Some(unsafe { CStr::from_ptr(image_dir).to_string_lossy().into_owned() })
    } else {
        None
    };

    let config_json_str = if !config_json.is_null() {
        Some(unsafe { CStr::from_ptr(config_json).to_string_lossy().into_owned() })
    } else {
        None
    };

    let info = StreamInfo {
        mimetype: mimetype_str,
        extension: extension_str,
        filename: filename_str,
        image_dir: image_dir_str,
        embed_images: embed_images != 0,
        extra: config_json_str,
        ..Default::default()
    };

    let void2md = Void2md::new();
    match void2md.convert(buffer, &info) {
        Ok(result) => CStringResult::new(result.markdown, result.title),
        Err(e) => CStringResult::error(&e.to_string()),
    }
}

/// Convert a file to markdown
///
/// # Arguments
/// * `file_path` - Path to the file to convert
/// * `mimetype` - Optional mimetype (can be null)
/// * `extension` - Optional file extension (can be null)
/// * `filename` - Optional filename (can be null)
/// * `image_dir` - Optional image directory (can be null)
/// * `embed_images` - Whether to embed images as base64 data URLs (1 = true, 0 = false)
///
/// # Returns
/// * `CStringResult` - Contains markdown string, optional title, and success flag
#[no_mangle]
pub extern "C" fn void2md_convert_file(
    file_path: *const c_char,
    mimetype: *const c_char,
    extension: *const c_char,
    filename: *const c_char,
    image_dir: *const c_char,
    embed_images: c_int,
) -> CStringResult {
    // Safety: We assume the caller provides valid pointer
    let path_str = if !file_path.is_null() {
        match unsafe { CStr::from_ptr(file_path).to_str() } {
            Ok(s) => s.to_string(),
            Err(_) => return CStringResult::error("Invalid file path encoding"),
        }
    } else {
        return CStringResult::error("File path is null");
    };

    let mimetype_str = if !mimetype.is_null() {
        Some(unsafe { CStr::from_ptr(mimetype).to_string_lossy().into_owned() })
    } else {
        None
    };

    let extension_str = if !extension.is_null() {
        Some(unsafe { CStr::from_ptr(extension).to_string_lossy().into_owned() })
    } else {
        None
    };

    let filename_str = if !filename.is_null() {
        Some(unsafe { CStr::from_ptr(filename).to_string_lossy().into_owned() })
    } else {
        None
    };

    let image_dir_str = if !image_dir.is_null() {
        Some(unsafe { CStr::from_ptr(image_dir).to_string_lossy().into_owned() })
    } else {
        None
    };

    let info = StreamInfo {
        mimetype: mimetype_str,
        extension: extension_str,
        filename: filename_str,
        image_dir: image_dir_str,
        local_path: Some(path_str.clone()),
        embed_images: embed_images != 0,
        ..Default::default()
    };

    let void2md = Void2md::new();
    match void2md.convert_file_with(&path_str, info) {
        Ok(result) => CStringResult::new(result.markdown, result.title),
        Err(e) => CStringResult::error(&e.to_string()),
    }
}

/// Free a CString returned by the library
///
/// # Arguments
/// * `ptr` - Pointer to CString to free
#[no_mangle]
pub extern "C" fn void2md_free_string(ptr: *mut c_char) {
    if !ptr.is_null() {
        unsafe {
            let _ = CString::from_raw(ptr);
        }
    }
}

/// Get supported converter names
///
/// # Returns
/// * Comma-separated list of supported converter names
#[no_mangle]
pub extern "C" fn void2md_converter_names() -> *mut c_char {
    let void2md = Void2md::new();
    let names = void2md.converter_names().join(",");
    let cstr = CString::new(names).unwrap();
    cstr.into_raw()
}