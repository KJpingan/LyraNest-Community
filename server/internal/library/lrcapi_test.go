package library

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLrcAPIClientDisabledWhenBaseURLEmpty(t *testing.T) {
	client := NewLrcAPIClient("", "secret", time.Second)
	if client.Enabled() {
		t.Fatalf("Enabled() = true, want false when baseURL is empty")
	}
	_, err := client.FetchLyrics(context.Background(), "Song", "Artist")
	if !errors.Is(err, ErrLyricsNotFound) {
		t.Fatalf("FetchLyrics err = %v, want ErrLyricsNotFound", err)
	}
}

func TestLrcAPIClientFetchSuccess(t *testing.T) {
	var receivedAuth string
	var receivedTitle, receivedArtist string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/lyrics/single" {
			t.Fatalf("path = %q, want /api/v1/lyrics/single", r.URL.Path)
		}
		receivedAuth = r.Header.Get("Authorization")
		receivedTitle = r.URL.Query().Get("title")
		receivedArtist = r.URL.Query().Get("artist")
		_, _ = w.Write([]byte("[00:01.00]hello lyrics\n"))
	}))
	defer server.Close()

	client := NewLrcAPIClient(server.URL, "secret-key", 2*time.Second)
	content, err := client.FetchLyrics(context.Background(), "孤勇者", "Eason Chan")
	if err != nil {
		t.Fatalf("FetchLyrics returned error: %v", err)
	}
	if content != "[00:01.00]hello lyrics" {
		t.Fatalf("content = %q", content)
	}
	if receivedAuth != "secret-key" {
		t.Fatalf("Authorization header = %q, want secret-key", receivedAuth)
	}
	if receivedTitle != "孤勇者" {
		t.Fatalf("title = %q", receivedTitle)
	}
	if receivedArtist != "Eason Chan" {
		t.Fatalf("artist = %q", receivedArtist)
	}
}

func TestLrcAPIClientFetch404ReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Lyrics not found."))
	}))
	defer server.Close()

	client := NewLrcAPIClient(server.URL, "", time.Second)
	_, err := client.FetchLyrics(context.Background(), "Unknown", "Nobody")
	if !errors.Is(err, ErrLyricsNotFound) {
		t.Fatalf("err = %v, want ErrLyricsNotFound", err)
	}
}

func TestLrcAPIClientFetch500ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer server.Close()

	client := NewLrcAPIClient(server.URL, "", time.Second)
	_, err := client.FetchLyrics(context.Background(), "Song", "Artist")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if errors.Is(err, ErrLyricsNotFound) {
		t.Fatalf("err should not be ErrLyricsNotFound on 500")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v, want mention of status 500", err)
	}
}

func TestLrcAPIClientNoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("[00:00.00]"))
	}))
	defer server.Close()

	client := NewLrcAPIClient(server.URL, "", time.Second)
	if _, err := client.FetchLyrics(context.Background(), "Song", ""); err != nil {
		t.Fatalf("FetchLyrics error: %v", err)
	}
	if receivedAuth != "" {
		t.Fatalf("Authorization = %q, want empty", receivedAuth)
	}
}

func TestLrcAPIClientSkipsWhenTitleAndArtistEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be hit when title and artist are empty")
	}))
	defer server.Close()

	client := NewLrcAPIClient(server.URL, "", time.Second)
	_, err := client.FetchLyrics(context.Background(), "  ", "")
	if !errors.Is(err, ErrLyricsNotFound) {
		t.Fatalf("err = %v, want ErrLyricsNotFound", err)
	}
}
