package hash

import (
	"bytes"
	"crypto/sha1"
	"io"
)

func HashSHA1(b []byte) ([]byte, error) {
	h := sha1.New()
	rdr := bytes.NewReader(b)
	if _, err := io.Copy(h, rdr); err != nil {
		return nil, err
	}

	hash := h.Sum(nil)
	return hash, nil
}

/*
https://wiki.theory.org/BitTorrentSpecification#Tracker_HTTP.2FHTTPS_Protocol

"bytes not in the set 0-9, a-z, A-Z, '.', '-', '_' and '~', must be encoded using the "%nn" format, where nn is the hexadecimal value of the byte."
*/
func URLSanitize(input []byte) string {
	escaped := make([]byte, 0, 20*3)
	hexDigits := "0123456789ABCDEF"

	allowedSpecial := []byte{'.', '-', '_', '~'}

	for _, b := range input {
		if (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || bytes.Contains(allowedSpecial, []byte{b}) {
			// For printable ASCII characters that are not reserved, append them as is
			escaped = append(escaped, b)
		} else {
			escaped = append(escaped, '%', hexDigits[b>>4], hexDigits[b&0x0F])
		}
	}

	return string(escaped)
}
