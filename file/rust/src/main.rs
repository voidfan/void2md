use std::process::ExitCode;

use clap::{Parser, Subcommand};

use void2md::commands::convert::{convert, ConvertOptions};
use void2md::commands::formats::formats;
use void2md::commands::onboard::onboard;
use void2md::utils::output::{error, OutputOptions};

// clap renders "--version" as "{name} {version}".
const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Parser)]
#[command(
    name = "void2md",
    about = "Convert anything to markdown.",
    version = VERSION,
    after_help = r#"Examples:
  $ void2md report.pdf                  Convert a PDF to markdown
  $ void2md document.docx -o doc.md     Convert DOCX, write to file
  $ void2md https://example.com         Convert a web page
  $ void2md photo.jpg                   Extract EXIF metadata
  $ void2md recording.mp3               Extract audio metadata
  $ cat file.pdf | void2md -            Read from stdin

Docs: https://github.com/shift-labs-ai/void2md"#
)]
struct Cli {
    /// File path, URL, or - for stdin (default command: convert)
    source: Option<String>,

    #[command(subcommand)]
    command: Option<Commands>,

    /// Output as JSON
    #[arg(long, global = true)]
    json: bool,

    /// Raw markdown only, no decoration
    #[arg(short, long, global = true)]
    quiet: bool,

    /// Write to file instead of stdout
    #[arg(short, long, global = true)]
    output: Option<String>,

    /// Extract images to this directory
    #[arg(short = 'i', long = "image-dir", global = true)]
    image_dir: Option<String>,

    /// Skip image extraction; images become placeholder comments
    #[arg(long = "no-images", global = true, conflicts_with = "image_dir")]
    no_images: bool,
}

#[derive(Subcommand)]
enum Commands {
    /// Convert a file or URL to markdown
    #[command(alias = "c")]
    Convert {
        /// File path, URL, or - for stdin
        source: String,

        /// Write to file instead of stdout
        #[arg(short, long)]
        output: Option<String>,
    },
    /// List supported formats
    Formats,
    /// Add void2md instructions to CLAUDE.md or AGENTS.md
    Onboard,
}

const NO_ARGS_HELP: &str = "void2md — convert anything to markdown

Usage:  void2md <file-or-url> [options]

Examples:
  $ void2md report.pdf
  $ void2md document.docx -o doc.md
  $ void2md https://example.com

Commands:
  void2md formats     List supported formats
  void2md onboard     Add instructions to CLAUDE.md

Run void2md --help for all options.
Docs: https://github.com/shift-labs-ai/void2md";

const KNOWN_COMMANDS: &[&str] = &["convert", "formats", "onboard", "help"];

fn levenshtein(a: &str, b: &str) -> usize {
    let ac: Vec<char> = a.chars().collect();
    let bc: Vec<char> = b.chars().collect();
    let m = ac.len();
    let n = bc.len();
    let mut dp = vec![vec![0usize; n + 1]; m + 1];
    for (i, row) in dp.iter_mut().enumerate() {
        row[0] = i;
    }
    for j in 0..=n {
        dp[0][j] = j;
    }
    for i in 0..m {
        for j in 0..n {
            let cost = usize::from(ac[i] != bc[j]);
            dp[i + 1][j + 1] = (dp[i][j + 1] + 1)
                .min(dp[i + 1][j] + 1)
                .min(dp[i][j] + cost);
        }
    }
    dp[m][n]
}

fn main() -> ExitCode {
    // No args → show concise help
    let args: Vec<String> = std::env::args().collect();
    if args.len() <= 1 {
        println!("{}", NO_ARGS_HELP);
        return ExitCode::SUCCESS;
    }

    // Pre-parse: check for typos against known subcommands before clap parses.
    // This matches the TS behavior of program.on("command:*", ...) which handles
    // unknown commands that look like typos of known ones.
    if args.len() >= 2 {
        let first_arg = &args[1];
        // Only check if it doesn't look like a flag, file path, or URL
        if !first_arg.starts_with('-')
            && !first_arg.contains('/')
            && !first_arg.contains('.')
            && !first_arg.starts_with("http")
        {
            // Check if it's NOT a known command/alias — if so check for typos
            let is_known = matches!(
                first_arg.as_str(),
                "convert" | "c" | "formats" | "onboard" | "help"
            );
            if !is_known {
                let close: Vec<&&str> = KNOWN_COMMANDS
                    .iter()
                    .filter(|c| levenshtein(first_arg, c) <= 2 && first_arg.as_str() != **c)
                    .collect();
                if !close.is_empty() {
                    error(&format!(
                        "Unknown command '{}'. Did you mean '{}'?",
                        first_arg, close[0]
                    ));
                    return ExitCode::from(1);
                }
            }
        }
    }

    let cli = Cli::parse();

    match cli.command {
        Some(Commands::Formats) => {
            let opts = OutputOptions {
                json: cli.json,
                quiet: cli.quiet,
            };
            formats(&opts);
            ExitCode::SUCCESS
        }
        Some(Commands::Onboard) => {
            let opts = OutputOptions {
                json: cli.json,
                quiet: cli.quiet,
            };
            onboard(&opts);
            ExitCode::SUCCESS
        }
        Some(Commands::Convert {
            source,
            output: out_override,
        }) => {
            let code = convert(
                &source,
                &ConvertOptions {
                    json: cli.json,
                    quiet: cli.quiet,
                    output_file: out_override.or(cli.output),
                    image_dir: cli.image_dir,
                    no_images: cli.no_images,
                },
            );
            ExitCode::from(code)
        }
        None => {
            // Default behavior: treat source as convert target
            match cli.source {
                Some(source) => {
                    let code = convert(
                        &source,
                        &ConvertOptions {
                            json: cli.json,
                            quiet: cli.quiet,
                            output_file: cli.output,
                            image_dir: cli.image_dir,
                            no_images: cli.no_images,
                        },
                    );
                    ExitCode::from(code)
                }
                None => {
                    println!("{}", NO_ARGS_HELP);
                    ExitCode::SUCCESS
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn levenshtein_identical_strings() {
        assert_eq!(levenshtein("hello", "hello"), 0);
    }

    #[test]
    fn levenshtein_single_edit() {
        assert_eq!(levenshtein("cat", "bat"), 1);
    }

    #[test]
    fn levenshtein_insertion() {
        assert_eq!(levenshtein("cat", "cats"), 1);
    }

    #[test]
    fn levenshtein_deletion() {
        assert_eq!(levenshtein("cats", "cat"), 1);
    }

    #[test]
    fn levenshtein_empty_strings() {
        assert_eq!(levenshtein("", ""), 0);
    }

    #[test]
    fn levenshtein_one_empty() {
        assert_eq!(levenshtein("abc", ""), 3);
        assert_eq!(levenshtein("", "abc"), 3);
    }

    #[test]
    fn levenshtein_typo_formts_formats() {
        assert_eq!(levenshtein("formts", "formats"), 1);
    }

    #[test]
    fn levenshtein_typo_formas_formats() {
        assert_eq!(levenshtein("formas", "formats"), 1);
    }

    #[test]
    fn levenshtein_typo_convet_convert() {
        assert_eq!(levenshtein("convet", "convert"), 1);
    }

    #[test]
    fn levenshtein_too_far() {
        assert!(levenshtein("xyz", "formats") > 2);
    }

    #[test]
    fn known_commands_match_ts() {
        assert_eq!(KNOWN_COMMANDS, &["convert", "formats", "onboard", "help"]);
    }

    #[test]
    fn no_args_help_starts_correctly() {
        assert!(NO_ARGS_HELP.starts_with("void2md — convert anything to markdown"));
    }

    #[test]
    fn no_args_help_contains_key_lines() {
        assert!(NO_ARGS_HELP.contains("Usage:  void2md <file-or-url> [options]"));
        assert!(NO_ARGS_HELP.contains("void2md formats"));
        assert!(NO_ARGS_HELP.contains("void2md onboard"));
        assert!(NO_ARGS_HELP.contains("Run void2md --help for all options."));
        assert!(NO_ARGS_HELP.contains("Docs: https://github.com/shift-labs-ai/void2md"));
    }

    #[test]
    fn version_string_matches_package() {
        assert_eq!(VERSION, env!("CARGO_PKG_VERSION"));
    }
}
