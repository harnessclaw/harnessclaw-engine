package skill

import (
	"fmt"
	"strings"
)

// SubstituteArguments replaces argument placeholders in skill content.
//
// Placeholders:
//   - $ARGUMENTS — the full argument string
//   - $0 — alias for $ARGUMENTS (the full string)
//   - $1, $2, ... — positional arguments (shell-style split)
//   - ${name} — named arguments (matched by argNames order)
//
// If appendIfNoPlaceholder is true and no placeholder is found,
// the args are appended to the end of the content.
func SubstituteArguments(content string, args string, appendIfNoPlaceholder bool, argNames []string) string {
	if args == "" {
		return content
	}

	hasPlaceholder := false

	// Replace $ARGUMENTS first.
	if strings.Contains(content, "$ARGUMENTS") {
		content = strings.ReplaceAll(content, "$ARGUMENTS", args)
		hasPlaceholder = true
	}

	// Split into positional arguments.
	parts := ParseArguments(args)

	// Replace named argument placeholders (${name}) before numbered ones
	// to avoid collision (e.g. ${file} won't be affected by $1 replacement).
	for i, name := range argNames {
		placeholder := fmt.Sprintf("${%s}", name)
		if strings.Contains(content, placeholder) {
			val := ""
			if i < len(parts) {
				val = parts[i]
			}
			content = strings.ReplaceAll(content, placeholder, val)
			hasPlaceholder = true
		}
	}

	// Replace numbered placeholders ($1, $2, ...) from highest to lowest
	// to avoid $1 clobbering $10, $11, etc.
	for i := len(parts); i >= 1; i-- {
		placeholder := fmt.Sprintf("$%d", i)
		if strings.Contains(content, placeholder) {
			content = replaceNonDigitSuffix(content, placeholder, parts[i-1])
			hasPlaceholder = true
		}
	}

	// Replace $0 last with word-boundary awareness (don't match $0 inside $01).
	if strings.Contains(content, "$0") {
		content = replaceNonDigitSuffix(content, "$0", args)
		hasPlaceholder = true
	}

	// Append if no placeholder was found.
	if !hasPlaceholder && appendIfNoPlaceholder {
		content = content + "\n\n" + args
	}

	return content
}

// replaceNonDigitSuffix replaces occurrences of placeholder in s with replacement,
// but only when the placeholder is NOT immediately followed by a digit.
// This prevents $1 from matching inside $10.
func replaceNonDigitSuffix(s, placeholder, replacement string) string {
	var result strings.Builder
	plen := len(placeholder)

	for i := 0; i < len(s); {
		// Check if placeholder matches at position i.
		if i+plen <= len(s) && s[i:i+plen] == placeholder {
			// Check the character after the placeholder.
			if i+plen < len(s) && s[i+plen] >= '0' && s[i+plen] <= '9' {
				// Followed by a digit — not a match, keep the original.
				result.WriteByte(s[i])
				i++
			} else {
				// Valid match — replace.
				result.WriteString(replacement)
				i += plen
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// ParseArguments splits an argument string respecting shell-style quoting.
// Supports double quotes and single quotes. Unquoted whitespace splits arguments.
func ParseArguments(args string) []string {
	var result []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for _, ch := range args {
		if escaped {
			current.WriteRune(ch)
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}

		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if (ch == ' ' || ch == '\t') && !inSingle && !inDouble {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteRune(ch)
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}
