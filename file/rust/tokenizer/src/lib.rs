use std::ffi::{c_char, c_int, CStr, CString};
use std::sync::OnceLock;

use serde_json::Value;
use tokenizers::Tokenizer;

static TOKENIZER: OnceLock<Tokenizer> = OnceLock::new();

/// Load the Qwen3-Embedding tokenizer from a JSON file path.
/// Returns 0 on success, -1 on error.
///
/// The tokenizer.json follows the HuggingFace format, where `model.merges`
/// must be a list of strings like `["Ġ Ġ", "i n", ...]`. Some sources
/// (e.g. certain qwen3-embedding exports) write `merges` as a list of
/// 2-element arrays like `[["Ġ", "Ġ"], ["i", "n"], ...]`, which the
/// `tokenizers` crate rejects during `Tokenizer::from_file`. To be robust,
/// if direct loading fails we detect that shape, convert each pair into
/// `"a b"` (preserving order == BPE rank), and load from the converted
/// JSON in memory.
#[no_mangle]
pub extern "C" fn init_tokenizer(path: *const c_char) -> c_int {
    if path.is_null() {
        return -1;
    }
    let c_str = unsafe { CStr::from_ptr(path) };
    let path_str = match c_str.to_str() {
        Ok(s) => s,
        Err(_) => return -1,
    };

    match Tokenizer::from_file(path_str) {
        Ok(tok) => {
            let _ = TOKENIZER.set(tok);
            0
        }
        Err(_) => {
            // Fallback: try to normalize `model.merges` from list-of-lists
            // to list-of-strings, then retry loading.
            match normalize_merges_and_load(path_str) {
                Ok(tok) => {
                    let _ = TOKENIZER.set(tok);
                    0
                }
                Err(_) => -1,
            }
        }
    }
}

/// Read the tokenizer.json file, and if `model.merges` is a list of
/// 2-element arrays, convert it to the standard list-of-strings form
/// before constructing the tokenizer.
fn normalize_merges_and_load(path: &str) -> Result<Tokenizer, String> {
    let content = std::fs::read_to_string(path).map_err(|e| e.to_string())?;
    let mut json: Value =
        serde_json::from_str(&content).map_err(|e| format!("json parse: {e}"))?;

    let merges = json
        .get_mut("model")
        .and_then(|m| m.get_mut("merges"))
        .ok_or_else(|| "missing model.merges".to_string())?;

    let needs_normalization = matches!(merges, Value::Array(items) if items.iter().any(|v| v.is_array()));
    if !needs_normalization {
        // No conversion needed; the original file is already in the
        // standard format, so the failure must be something else.
        return Err("merges already in string form".to_string());
    }

    let converted: Vec<Value> = merges
        .as_array()
        .ok_or_else(|| "merges is not an array".to_string())?
        .iter()
        .map(|pair| {
            match pair {
                Value::Array(parts) if parts.len() == 2 => {
                    let a = parts[0].as_str().unwrap_or_default();
                    let b = parts[1].as_str().unwrap_or_default();
                    Value::String(format!("{a} {b}"))
                }
                Value::String(s) => Value::String(s.clone()),
                other => Value::String(other.to_string()),
            }
        })
        .collect();

    *merges = Value::Array(converted);

    // Re-serialize the full document and reload from the string.
    let json_str = serde_json::to_string(&json).map_err(|e| e.to_string())?;
    Tokenizer::from_bytes(json_str.as_bytes()).map_err(|e| e.to_string())
}

/// Count BPE tokens in the given text (without special tokens).
/// Returns the token count, or -1 on error (including if tokenizer is not initialized).
#[no_mangle]
pub extern "C" fn count_tokens(text: *const c_char) -> c_int {
    if text.is_null() {
        return -1;
    }
    let c_str = unsafe { CStr::from_ptr(text) };
    let text_str = match c_str.to_str() {
        Ok(s) => s,
        Err(_) => return -1,
    };
    if text_str.is_empty() {
        return 0;
    }

    let tok = match TOKENIZER.get() {
        Some(t) => t,
        None => return -1, // tokenizer not initialized, return error instead of panicking
    };
    match tok.encode(text_str, false) {
        Ok(encoding) => encoding.get_ids().len() as c_int,
        Err(_) => -1,
    }
}

/// Batch encode: for each of `count` strings in `texts`, compute token count
/// and write it into `out_lengths`. Returns 0 on success, -1 on error.
///
/// # Safety
/// `texts` must point to an array of `count` valid, null-terminated C strings.
/// `out_lengths` must point to a buffer of at least `count` u32 elements.
#[no_mangle]
pub unsafe extern "C" fn encode_batch(
    texts: *const *const c_char,
    count: c_int,
    out_lengths: *mut u32,
) -> c_int {
    if texts.is_null() || out_lengths.is_null() || count <= 0 {
        return -1;
    }

    let tok = match TOKENIZER.get() {
        Some(t) => t,
        None => return -1, // tokenizer not initialized
    };
    let n = count as usize;

    for i in 0..n {
        let s_ptr = *texts.add(i);
        let len = if s_ptr.is_null() {
            0u32
        } else {
            let c_str = CStr::from_ptr(s_ptr);
            match c_str.to_str() {
                Ok(s) if s.is_empty() => 0u32,
                Ok(s) => match tok.encode(s, false) {
                    Ok(encoding) => encoding.get_ids().len() as u32,
                    Err(_) => 0u32,
                },
                Err(_) => 0u32,
            }
        };
        *out_lengths.add(i) = len;
    }

    0
}

/// Free a string previously allocated by Rust (e.g. returned from encode_batch).
///
/// # Safety
/// `s` must have been allocated by Rust's CString.
#[no_mangle]
pub unsafe extern "C" fn free_string(s: *mut c_char) {
    if !s.is_null() {
        let _ = CString::from_raw(s);
    }
}
