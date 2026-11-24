package access

import (
	"strings"
	"unicode"
)

// asciiSymbolOrder lists the ASCII punctuation and symbols in the order
// the Unicode collation algorithm gives them.
const asciiSymbolOrder = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

// collationWeight returns the class and position of a character for the
// first level of comparison: white space, then punctuation and symbols,
// then digits, then letters without regard to case.
func collationWeight(r rune) (class int, position rune) {
	switch {
	case unicode.IsSpace(r):
		return 0, r
	case unicode.IsDigit(r):
		return 2, r
	case unicode.IsLetter(r):
		return 3, unicode.ToLower(r)
	}
	if index := strings.IndexRune(asciiSymbolOrder, r); index >= 0 {
		return 1, rune(index)
	}
	return 1, r + rune(len(asciiSymbolOrder))
}

// localeCompare orders two strings as String.prototype.localeCompare of
// JavaScript does for ASCII text: by the characters without regard to
// case, with white space before punctuation before digits before
// letters, then lower case before upper case.
//
// The standard library has no Unicode collation, so characters outside
// ASCII are ordered by code point within their class.  Accented letters
// therefore sort after "z" instead of next to their base letter.
func localeCompare(a, b string) int {
	left, right := []rune(a), []rune(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		leftClass, leftPosition := collationWeight(left[i])
		rightClass, rightPosition := collationWeight(right[i])
		if leftClass != rightClass {
			return compareInt(leftClass, rightClass)
		}
		if leftPosition != rightPosition {
			return compareInt(int(leftPosition), int(rightPosition))
		}
	}
	if len(left) != len(right) {
		return compareInt(len(left), len(right))
	}
	for i := range left {
		if left[i] == right[i] {
			continue
		}
		if unicode.IsLower(left[i]) && unicode.IsUpper(right[i]) {
			return -1
		}
		if unicode.IsUpper(left[i]) && unicode.IsLower(right[i]) {
			return 1
		}
		return compareInt(int(left[i]), int(right[i]))
	}
	return 0
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
