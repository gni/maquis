package agent

import "strings"

const maxFallbackOpeningBytes = 4096

var fallbackOpeningMarkers = [...]string{
	"<tool_call",
	"<execute",
	"<tool:",
}

// fallbackToolTextFilter removes fallback XML tool invocations before assistant
// text reaches the terminal or conversation content. It retains only short
// delimiter prefixes across chunks, so tool payload size does not grow its
// internal buffer.
type fallbackToolTextFilter struct {
	pending                  string
	closingTag               string
	discardLeadingWhitespace bool
	emit                     func(string)
	flushed                  bool
}

func newFallbackToolTextFilter(emit func(string)) *fallbackToolTextFilter {
	return &fallbackToolTextFilter{emit: emit}
}

func (f *fallbackToolTextFilter) Write(chunk string) {
	if f == nil || f.flushed || chunk == "" {
		return
	}
	f.pending += chunk
	f.process(false)
}

func (f *fallbackToolTextFilter) Flush() {
	if f == nil || f.flushed {
		return
	}
	f.flushed = true
	f.process(true)
}

func (f *fallbackToolTextFilter) process(final bool) {
	for {
		if f.closingTag != "" {
			if closeIndex := strings.Index(f.pending, f.closingTag); closeIndex >= 0 {
				f.pending = f.pending[closeIndex+len(f.closingTag):]
				f.closingTag = ""
				f.discardLeadingWhitespace = true
				continue
			}

			if final {
				f.pending = ""
				f.closingTag = ""
				return
			}

			keep := longestSuffixMatchingPrefix(f.pending, f.closingTag)
			if keep == 0 {
				f.pending = ""
			} else {
				f.pending = f.pending[len(f.pending)-keep:]
			}
			return
		}

		if f.discardLeadingWhitespace {
			trimmed := 0
			for trimmed < len(f.pending) && isFallbackWhitespace(f.pending[trimmed]) {
				trimmed++
			}
			f.pending = f.pending[trimmed:]
			if f.pending == "" {
				return
			}
			f.discardLeadingWhitespace = false
		}

		openIndex := earliestFallbackOpening(f.pending)
		if openIndex < 0 {
			if final {
				f.emitText(f.pending)
				f.pending = ""
				return
			}

			keep := longestFallbackOpeningSuffix(f.pending)
			f.emitText(f.pending[:len(f.pending)-keep])
			if keep == 0 {
				f.pending = ""
			} else {
				f.pending = f.pending[len(f.pending)-keep:]
			}
			return
		}

		if openIndex > 0 {
			f.emitText(f.pending[:openIndex])
			f.pending = f.pending[openIndex:]
		}

		openEnd := strings.IndexByte(f.pending, '>')
		if openEnd < 0 {
			if final {
				f.pending = ""
				return
			}
			if len(f.pending) > maxFallbackOpeningBytes {
				f.emitText(f.pending[:1])
				f.pending = f.pending[1:]
				continue
			}
			return
		}

		opening := f.pending[:openEnd+1]
		closingTag, valid := fallbackClosingTag(opening)
		if !valid {
			f.emitText(f.pending[:1])
			f.pending = f.pending[1:]
			continue
		}

		f.pending = f.pending[openEnd+1:]
		f.closingTag = closingTag
	}
}

func (f *fallbackToolTextFilter) emitText(text string) {
	if text != "" && f.emit != nil {
		f.emit(text)
	}
}

func fallbackClosingTag(opening string) (string, bool) {
	switch {
	case strings.HasPrefix(opening, "<tool_call"):
		if !validFallbackOpeningBoundary(opening, len("<tool_call")) {
			return "", false
		}
		return "</tool_call>", true
	case strings.HasPrefix(opening, "<execute"):
		if !validFallbackOpeningBoundary(opening, len("<execute")) {
			return "", false
		}
		return "</execute>", true
	case strings.HasPrefix(opening, "<tool:"):
		name := opening[len("<tool:") : len(opening)-1]
		if name == "" {
			return "", false
		}
		for _, current := range name {
			if (current < 'a' || current > 'z') &&
				(current < 'A' || current > 'Z') &&
				(current < '0' || current > '9') &&
				current != '_' && current != '-' {
				return "", false
			}
		}
		return "</tool:" + name + ">", true
	default:
		return "", false
	}
}

func validFallbackOpeningBoundary(opening string, markerLength int) bool {
	if len(opening) <= markerLength {
		return false
	}
	next := opening[markerLength]
	return next == '>' || isFallbackWhitespace(next)
}

func earliestFallbackOpening(text string) int {
	earliest := -1
	for _, marker := range fallbackOpeningMarkers {
		index := strings.Index(text, marker)
		if index >= 0 && (earliest < 0 || index < earliest) {
			earliest = index
		}
	}
	return earliest
}

func longestFallbackOpeningSuffix(text string) int {
	longest := 0
	for _, marker := range fallbackOpeningMarkers {
		if matched := longestSuffixMatchingPrefix(text, marker); matched > longest {
			longest = matched
		}
	}
	return longest
}

func longestSuffixMatchingPrefix(text, marker string) int {
	maxLength := len(text)
	if len(marker)-1 < maxLength {
		maxLength = len(marker) - 1
	}
	for length := maxLength; length > 0; length-- {
		if strings.HasSuffix(text, marker[:length]) {
			return length
		}
	}
	return 0
}

func isFallbackWhitespace(current byte) bool {
	return current == ' ' || current == '\t' || current == '\r' || current == '\n'
}

// StripFallbackToolMarkup removes fallback tool protocol syntax from content
// loaded from older sessions.
func StripFallbackToolMarkup(content string) string {
	var filtered strings.Builder
	filter := newFallbackToolTextFilter(func(text string) {
		filtered.WriteString(text)
	})
	filter.Write(content)
	filter.Flush()
	return filtered.String()
}
