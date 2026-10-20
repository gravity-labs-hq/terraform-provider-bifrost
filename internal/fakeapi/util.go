package fakeapi

import (
	"io"
	"net/http"
	"net/url"
)

func urlUnescape(s string) (string, error) { return url.PathUnescape(s) }

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(io.LimitReader(r.Body, 1<<20)) }
