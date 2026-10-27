package sqlg

// escapedSequencesMysql mirrors MySQL's ESCAPED_SEQUENCES: control-char to backslash-escape form.
var escapedSequencesMysql = map[rune]string{
	'\a': "\\a",
	'\b': "\\b",
	'\f': "\\f",
	'\n': "\\n",
	'\r': "\\r",
	'\t': "\\t",
	'\v': "\\v",
	'\\': "\\\\",
}
