# Sync-Dir

A command-line tool written in Go to synchronize files and directories from a source path to a target path.

## Purpose

`sync-dir` treats the source directory as the "source of truth". It efficiently synchronizes the target directory to match the source by:

- **Adding:** Copying files/directories present in the source but missing in the target.
- **Updating:** Replacing files in the target that differ from the source. Differences are detected based on modification time and size. A SHA256 checksum is automatically used for verification if times differ but sizes match, or vice-versa.
- **Deleting:** Removing files/directories present in the target but no longer existing in the source.

## Features

- **Cross-Platform:** Compiles and runs on macOS, Windows, and Linux.
- **Efficient Comparison:** Uses modification times and file sizes for a quick initial comparison. Performs checksums only when necessary.
- **Concurrent Operations:** Scans source and target directories in parallel and performs file copy/delete operations concurrently (up to 10 operations at a time) for faster execution.
- **Exclusions:** Supports excluding files and directories using `.gitignore` style patterns via:
    - A `.sync-ignore` file placed in the **root of the source directory**.
    - One or more `--exclude` (or `-e`) flags.
- **User Confirmation:** Displays a summary plan (counts of adds, updates, deletes) and a sample of specific actions before proceeding. Requires user confirmation (defaults to 'yes').
- **Dry Run Mode:** Use the `--dry-run` flag to see what actions *would* be taken without making any actual changes to the filesystem.
- **Progress Indicators:** Shows progress bars during scanning and file synchronization phases.

## Installation

Ensure you have Go installed (version 1.18 or later recommended).

```bash
go install github.com/jeepinbird/sync-dir@latest
```

This will download the source code, compile it, and place the `sync-dir` executable in your `$GOPATH/bin` directory. Make sure this directory is in your system's `PATH`.

## Usage

`sync-dir <source_directory> <target_directory> [flags]`

**Arguments**:

- `<source_directory>`: The path to the directory to sync from (the source of truth).
- `<target_directory>`: The path to the directory to sync to. It will be modified to match the source. If it doesn't exist, it will be created.

**Flags**:

- `-e, --exclude <pattern>`: Specify a pattern to exclude. Can be used multiple times. Uses `.gitignore` syntax (e.g., `*.log`, `temp/`, `**/node_modules`).
- `--dry-run`: Perform a trial run without making any changes. Shows the planned actions.

**Examples**:

```bash
# Sync contents of ./my-project to /backup/my-project, excluding build artifacts and logs
sync-dir --exclude "dist/" --exclude "*.log" ./my-project /backup/my-project

# Perform a dry run first
sync-dir --dry-run ./my-project /backup/my-project

# Sync using exclusions from .sync-ignore file in ./my-project
sync-dir ./my-project /backup/my-project
```

### Using `.sync-ignore` File

Create a file named `.sync-ignore` in the root of your source directory. Add patterns (one per line) of files or directories you wish to exclude from the synchronization, following the same syntax as `.gitignore`.

**Example `.sync-ignore`**:
```bash
# Ignore build outputs
/dist/
/build/

# Ignore log files anywhere
*.log

# Ignore specific configuration
config.local.yaml

# Ignore vendor directories
**/vendor/
**/node_modules/
```

## Building from Source

Clone the repository (if you haven't already) and run `go build`:
```bash
git clone https://github.com/jeepinbird/sync-dir.git
cd sync-dir
go build
```
