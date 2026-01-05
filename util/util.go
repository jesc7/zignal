package util

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"math/rand"
	"os"
	"strings"
)

func IsFileExists(filename string) bool {
	if _, e := os.Stat(filename); errors.Is(e, os.ErrNotExist) {
		return false
	}
	return true
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func RandomString(n int, set string) string {
	if set == "" {
		set = charset
	}
	sb := strings.Builder{}
	sb.Grow(n)
	for i, l := 0, len(set); i < n; i++ {
		sb.WriteByte(set[rand.Intn(l)])
	}
	return sb.String()
}

func Iif[T any](b bool, v1, v2 T) T {
	if b {
		return v1
	}
	return v2
}

func Zip(in []byte) ([]byte, error) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	_, e := gz.Write(in)
	if e != nil {
		return []byte{}, e
	}
	if e = gz.Flush(); e != nil {
		return []byte{}, e
	}
	if e = gz.Close(); e != nil {
		return []byte{}, e
	}
	return b.Bytes(), nil
}

func Unzip(in []byte) ([]byte, error) {
	var b bytes.Buffer
	_, e := b.Write(in)
	if e != nil {
		return []byte{}, e
	}
	r, e := gzip.NewReader(&b)
	if e != nil {
		return []byte{}, e
	}
	res, e := io.ReadAll(r)
	if e != nil {
		return []byte{}, e
	}
	return res, nil
}
