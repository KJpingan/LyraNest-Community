package library

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LrcAPIClient fetches standard LRC lyrics from a HisAtri/LrcApi compatible
// endpoint. The service returns raw LRC text on 200 and "Lyrics not found."
// on 404. When API_AUTH is configured on the server side, the same key must be
// supplied via the Authorization header; an empty key disables auth.
type LrcAPIClient struct {
	baseURL string
	authKey string
	http    *http.Client
}

// NewLrcAPIClient returns a client that calls the LrcApi service at baseURL.
// baseURL may be empty, in which case the client is still constructed but
// FetchLyrics returns ErrLyricsNotFound without making a request. Callers can
// use this to keep wiring unconditional.
func NewLrcAPIClient(baseURL, authKey string, timeout time.Duration) *LrcAPIClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &LrcAPIClient{
		baseURL: baseURL,
		authKey: strings.TrimSpace(authKey),
		http:    &http.Client{Timeout: timeout},
	}
}

// Enabled reports whether the client has a target base URL configured.
func (c *LrcAPIClient) Enabled() bool {
	return c != nil && c.baseURL != ""
}

// FetchLyrics requests LRC lyrics for the given title and artist. When the
// LrcApi service responds 404 (or baseURL is unset), ErrLyricsNotFound is
// returned so callers can treat it uniformly with the local-miss path.
func (c *LrcAPIClient) FetchLyrics(ctx context.Context, title, artist string) (string, error) {
	if !c.Enabled() {
		return "", ErrLyricsNotFound
	}
	if strings.TrimSpace(title) == "" && strings.TrimSpace(artist) == "" {
		return "", ErrLyricsNotFound
	}

	endpoint := c.baseURL + "/api/v1/lyrics/single"
	query := url.Values{}
	if title := strings.TrimSpace(title); title != "" {
		query.Set("title", title)
	}
	if artist := strings.TrimSpace(artist); artist != "" {
		query.Set("artist", artist)
	}
	requestURL := endpoint + "?" + query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", fmt.Errorf("build lrcapi request: %w", err)
	}
	if c.authKey != "" {
		request.Header.Set("Authorization", c.authKey)
	}
	request.Header.Set("Accept", "text/plain, text/plain; charset=utf-8, */*")

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("lrcapi request failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxLyricsFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read lrcapi response: %w", err)
	}
	if int64(len(body)) > maxLyricsFileBytes {
		return "", ErrLyricsTooLarge
	}

	switch response.StatusCode {
	case http.StatusOK:
		content := strings.TrimSpace(string(body))
		if content == "" {
			return "", ErrLyricsNotFound
		}
		return content, nil
	case http.StatusNotFound:
		return "", ErrLyricsNotFound
	default:
		return "", fmt.Errorf("lrcapi returned status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
}
