package main

import (
	"bytes"
	"crypto/sha1"
	"io"
)

func HashSHA1(b []byte) []byte {
	h := sha1.New()
	rdr := bytes.NewReader(b)
	if _, err := io.Copy(h, rdr); err != nil {
		panic(err)
	}

	hash := h.Sum(nil)
	return hash
}

func CustomURLEscape(input []byte) string {
	escaped := make([]byte, 0, 3*len(input))
	hexDigits := "0123456789abcdef"
	urlReserved := map[byte]bool{
		'#':  true,
		'=':  true,
		'/':  true,
		'?':  true,
		';':  true,
		':':  true,
		'@':  true,
		'<':  true,
		'>':  true,
		'"':  true,
		'&':  true,
		'{':  true,
		'}':  true,
		'|':  true,
		'\\': true,
		'^':  true,
		'~':  true,
		'[':  true,
		']':  true,
		'`':  true,
		'\'': true,
		'$':  true,
		'-':  true,
		'_':  true,
		'.':  true,
		'+':  true,
		'!':  true,
		'*':  true,
		'(':  true,
		')':  true,
		',':  true,
	}
	for _, b := range input {
		if b >= 0x20 && b <= 0x7E && !urlReserved[b] {
			// For printable ASCII characters that are not reserved, append them as is
			escaped = append(escaped, b)
		} else {
			// Append '%' followed by the hexadecimal representation of the byte
			escaped = append(escaped, '%', hexDigits[b>>4], hexDigits[b&0x0F])
		}
	}
	return string(escaped)
}
