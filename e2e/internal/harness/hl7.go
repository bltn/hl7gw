package harness

import "strings"

const mshID = "MSH"

func SetControlID(msg, id string) string {
	head, tail := msg, ""
	if end := strings.IndexAny(msg, "\r\n"); end >= 0 {
		head, tail = msg[:end], msg[end:]
	}

	fields := strings.Split(head, "|")
	if fields[0] != mshID || len(fields) <= 9 {
		return msg
	}
	fields[9] = id
	return strings.Join(fields, "|") + tail
}

func Field(msg, segmentID string, n int) (string, bool) {
	sep := "|"
	if strings.HasPrefix(msg, mshID) && len(msg) > len(mshID) {
		sep = msg[3:4]
	}
	idx := n
	if segmentID == mshID {
		idx = n - 1
	}
	for _, seg := range strings.FieldsFunc(msg, func(r rune) bool { return r == '\r' || r == '\n' }) {
		fields := strings.Split(seg, sep)
		if fields[0] != segmentID {
			continue
		}
		if idx < 1 || idx >= len(fields) {
			return "", false
		}
		return fields[idx], true
	}
	return "", false
}

func GiantMessage(fieldBytes int) string {
	return "MSH|^~\\&|" + strings.Repeat("A", fieldBytes) +
		"|SENDFAC|RECVAPP|RECVFAC|20240101120000||ADT^A01|MSG00001|P|2.5"
}

func NormalizeACK(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimRight(s, "\n")

	segments := strings.Split(s, "\n")
	for i, seg := range segments {
		fields := strings.Split(seg, "|")
		if fields[0] != mshID {
			continue
		}
		if len(fields) > 6 && isTimestamp(fields[6]) {
			fields[6] = "<TS>"
		}
		if len(fields) > 9 && fields[9] != "" {
			fields[9] = "<CID>"
		}
		segments[i] = strings.Join(fields, "|")
	}
	return strings.Join(segments, "\n")
}

func isTimestamp(s string) bool {
	if len(s) != 14 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
