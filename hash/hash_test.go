package hash_test

import (
	"testing"

	"github.com/esmakov/bittorrent-client/hash"
)

func TestURLSanitize(t *testing.T) {
	input := []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf1, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x9a}

	expected := "%124Vx%9A%BC%DE%F1%23Eg%89%AB%CD%EF%124Vx%9A"

	result := hash.URLSanitize([]byte(input))
	if result != expected {
		t.Fatalf("Expected: %v\n Got: %v\n", expected, result)
	}
}
