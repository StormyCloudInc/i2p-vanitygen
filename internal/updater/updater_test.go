package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAssetSHA256(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  other.bin")
		fmt.Fprintln(w, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  vanitygenerator_linux_signed")
	}))
	defer server.Close()

	hash, err := fetchAssetSHA256(context.Background(), server.URL, "vanitygenerator_linux_signed")
	if err != nil {
		t.Fatalf("fetchAssetSHA256 returned error: %v", err)
	}

	want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if hash != want {
		t.Fatalf("unexpected hash: got %q, want %q", hash, want)
	}
}

func TestFetchAssetSHA256MissingAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  another.bin")
	}))
	defer server.Close()

	_, err := fetchAssetSHA256(context.Background(), server.URL, "vanitygenerator_linux_signed")
	if err == nil {
		t.Fatal("expected error for missing asset checksum")
	}
}

func TestFetchAssetSHA256InvalidChecksumLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "abc  vanitygenerator_linux_signed")
	}))
	defer server.Close()

	_, err := fetchAssetSHA256(context.Background(), server.URL, "vanitygenerator_linux_signed")
	if err == nil {
		t.Fatal("expected error for invalid checksum length")
	}
}
