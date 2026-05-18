// source_path.go isolates the security-critical filesystem read path
// behind ArtifactWrite's new source_path field. Keeping this in its own
// file makes the threat model auditable in one place and prevents the
// validation logic from drifting if write.go is refactored.
//
// The threat model:
//
//   ArtifactWrite is LLM-callable, so a misaligned / prompt-injected /
//   buggy agent could supply an arbitrary path. The mitigations are:
//
//   1. Absolute path required — relative paths are ambiguous (depend on
//      server CWD) and lend themselves to "../"-style traversal.
//   2. Symlink resolution (EvalSymlinks) before the prefix check, so a
//      symlink inside workspace pointing at /etc/passwd is caught.
//   3. Prefix check against an explicit allow-list of directories (the
//      server's workspace and configured skill directories). Anything
//      else is rejected.
//   4. Hard size cap. base64-encoding a 1 GB file into an artifact would
//      OOM the server and produce a useless artifact.
//   5. Regular file only — refuse devices, FIFOs, directories.
//
// The allow-list is wired at WriteTool construction time, NOT taken from
// LLM input. The LLM cannot change it.

package artifacttool

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxSourcePathBytes caps the file size readable via source_path. 50 MB
// is large enough for most generated artifacts (docx / pdf / single image
// / mid-sized csv) while being orders of magnitude below the server's
// memory budget. base64 encoding inflates by ~33%, so 50 MB on disk
// becomes ~67 MB in the artifact store.
const maxSourcePathBytes int64 = 50 * 1024 * 1024

// readFromAllowedPath validates path against allowedDirs and reads the
// file when validation passes. Returns the raw bytes plus the resolved
// absolute path (post-symlink) so callers can log what they actually
// read.
//
// allowedDirs must be canonicalised absolute paths (EvalSymlinks'd at
// WriteTool construction time). An empty allowedDirs slice rejects every
// path — fail-closed.
func readFromAllowedPath(path string, allowedDirs []string) (data []byte, resolved string, err error) {
	if path == "" {
		return nil, "", fmt.Errorf("source_path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("source_path must be absolute (got %q)", path)
	}
	if len(allowedDirs) == 0 {
		return nil, "", fmt.Errorf("source_path is not configured on this server " +
			"(no allowed directories registered)")
	}

	resolved, err = filepath.EvalSymlinks(path)
	if err != nil {
		// EvalSymlinks fails for non-existent files; collapse to a clean
		// "not found" so the LLM gets an actionable message and we don't
		// leak filesystem details.
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("source_path does not exist: %s", path)
		}
		return nil, "", fmt.Errorf("source_path resolve failed: %w", err)
	}

	if !pathHasAnyPrefix(resolved, allowedDirs) {
		return nil, "", fmt.Errorf(
			"source_path %q is outside the allowed directories. "+
				"Only files under the configured workspace / skills dirs are readable",
			path,
		)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("source_path stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("source_path %q is not a regular file "+
			"(directories, devices, sockets are not allowed)", path)
	}
	if info.Size() > maxSourcePathBytes {
		return nil, "", fmt.Errorf(
			"source_path file size %d bytes exceeds %d MB limit",
			info.Size(), maxSourcePathBytes/(1024*1024),
		)
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("source_path open: %w", err)
	}
	defer f.Close()

	// LimitReader is belt-and-braces: even if a race made the file grow
	// after Stat, we still bound memory.
	data, err = io.ReadAll(io.LimitReader(f, maxSourcePathBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("source_path read: %w", err)
	}
	if int64(len(data)) > maxSourcePathBytes {
		return nil, "", fmt.Errorf(
			"source_path file grew past %d MB limit during read",
			maxSourcePathBytes/(1024*1024),
		)
	}
	return data, resolved, nil
}

// pathHasAnyPrefix reports whether candidate's directory hierarchy
// starts with any of allowed. Both candidate and allowed are expected to
// be absolute, symlink-resolved paths.
//
// Uses filepath.Rel for safety: rel-result starting with ".." means the
// candidate is outside that root. Plain HasPrefix would accept
// "/workspace_evil" when allowed=["/workspace"], so we don't use it.
func pathHasAnyPrefix(candidate string, allowed []string) bool {
	candidate = filepath.Clean(candidate)
	for _, root := range allowed {
		root = filepath.Clean(root)
		// Same path is allowed (matches Rel returning ".").
		if candidate == root {
			return true
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		// On unix, Rel returns "../foo" when candidate escapes root.
		if strings.HasPrefix(rel, "..") {
			continue
		}
		// Windows: rel containing volume separator means cross-drive.
		if rel == "." || !strings.Contains(rel, "..") {
			return true
		}
	}
	return false
}

// canonicaliseAllowedDirs is called at WriteTool construction. It
// EvalSymlinks each dir and drops anything that fails to resolve. The
// resulting slice is used as-is in readFromAllowedPath — runtime adds
// zero allocations for path validation hot path beyond filepath.Clean.
func canonicaliseAllowedDirs(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			// Dir doesn't exist yet at boot — keep the absolute form so
			// once the user creates it, source_path reads start working.
			// (Pre-resolve is a perf optimisation, not a correctness gate.)
			resolved = abs
		}
		resolved = filepath.Clean(resolved)
		if !seen[resolved] {
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	return out
}
