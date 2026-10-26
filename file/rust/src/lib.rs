/// The PDF pipeline makes many short-lived small allocations (parse
/// trees, text runs, decode buffers); mimalloc cuts their cost roughly
/// in half versus the system allocator. Applies to the CLI and the
/// napi addon alike.
#[global_allocator]
static GLOBAL: mimalloc::MiMalloc = mimalloc::MiMalloc;

pub mod commands;
pub mod config;
pub mod converters;
pub mod discover_markdown_source;
pub mod void2md;
pub mod types;
pub mod utils;

#[cfg(feature = "napi")]
mod bindings;

// Always expose FFI for C-compatible bindings
pub mod ffi;
