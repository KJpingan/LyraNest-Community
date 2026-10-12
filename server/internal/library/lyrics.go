package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"music-player-server/internal/metadata"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var (
	ErrLyricsNotFound            = errors.New("lyrics not found")
	ErrUnsupportedLyricsEncoding = errors.New("unsupported lyrics encoding")
	ErrLyricsTooLarge            = errors.New("lyrics file exceeds the maximum allowed size")
	lyricsCandidateFileExtension = []string{".lrc", ".txt"}
)

const maxLyricsFileBytes = 1 << 20

type Lyrics struct {
	TrackID  string `json:"track_id"`
	Path     string `json:"-"`
	Encoding string `json:"encoding"`
	Source   string `json:"source"`
	Content  string `json:"content"`
}

type LyricsService struct {
	metadataReader metadata.Reader
	onlineFetcher  *LrcAPIClient
	onlineCache    *onlineLyricsCache
}

// onlineLyricsCache memoizes LRC text fetched from an online source so that
// subsequent playback / scrape requests for the same track do not hit the
// remote API repeatedly. The cache is in-memory only; entries live for the
// lifetime of the process. NAS deployments mount the music directory read-only,
// so we never persist fetched lyrics as sidecar files.
type onlineLyricsCache struct {
	entries map[string]string
}

func newOnlineLyricsCache() *onlineLyricsCache {
	return &onlineLyricsCache{entries: make(map[string]string)}
}

func (c *onlineLyricsCache) lookup(trackID string) (string, bool) {
	if c == nil {
		return "", false
	}
	value, ok := c.entries[trackID]
	return value, ok
}

func (c *onlineLyricsCache) store(trackID, content string) {
	if c == nil {
		return
	}
	c.entries[trackID] = content
}

func NewLyricsService() *LyricsService {
	return NewLyricsServiceWithMetadataReader(metadata.NewLocalReader())
}

func NewLyricsServiceWithMetadataReader(metadataReader metadata.Reader) *LyricsService {
	return &LyricsService{metadataReader: metadataReader}
}

// NewLyricsServiceWithOnline enables an online LrcApi fallback used when both
// sidecar and embedded lyrics are unavailable. metadataReader may be nil to
// skip embedded reads (defaults are still applied by the caller). onlineFetcher
// may be nil, in which case the service behaves like NewLyricsService().
func NewLyricsServiceWithOnline(metadataReader metadata.Reader, onlineFetcher *LrcAPIClient) *LyricsService {
	service := NewLyricsServiceWithMetadataReader(metadataReader)
	if onlineFetcher != nil && onlineFetcher.Enabled() {
		service.onlineFetcher = onlineFetcher
		service.onlineCache = newOnlineLyricsCache()
	}
	return service
}

func (s *LyricsService) ReadForTrack(track Track) (Lyrics, error) {
	return s.ReadForTrackContext(context.Background(), track)
}

func (s *LyricsService) ReadForTrackContext(ctx context.Context, track Track) (Lyrics, error) {
	path, err := FindLyricsPath(track.Path)
	if err == nil {
		data, err := readLyricsFile(path)
		if err != nil {
			return Lyrics{}, err
		}

		content, encodingName, err := DecodeLyrics(data)
		if err != nil {
			return Lyrics{}, err
		}

		return Lyrics{
			TrackID:  track.ID,
			Path:     path,
			Encoding: encodingName,
			Source:   "sidecar",
			Content:  content,
		}, nil
	}
	if !errors.Is(err, ErrLyricsNotFound) {
		return Lyrics{}, err
	}

	embedded, err := s.readEmbeddedLyrics(ctx, track)
	if err == nil {
		return embedded, nil
	}
	if !errors.Is(err, ErrLyricsNotFound) {
		return Lyrics{}, err
	}

	return s.readOnlineLyrics(ctx, track)
}

// readOnlineLyrics consults the in-memory cache first, then queries the
// configured LrcApi service. Network or 404 errors surface as
// ErrLyricsNotFound so the HTTP layer can return a consistent 404 to clients.
func (s *LyricsService) readOnlineLyrics(ctx context.Context, track Track) (Lyrics, error) {
	if s.onlineFetcher == nil {
		return Lyrics{}, ErrLyricsNotFound
	}

	if cached, ok := s.onlineCache.lookup(track.ID); ok {
		return Lyrics{
			TrackID:  track.ID,
			Encoding: "UTF-8",
			Source:   "lrcapi",
			Content:  cached,
		}, nil
	}

	content, err := s.onlineFetcher.FetchLyrics(ctx, track.Title, track.Artist)
	if err != nil {
		if errors.Is(err, ErrLyricsNotFound) {
			return Lyrics{}, ErrLyricsNotFound
		}
		slog.WarnContext(ctx, "lrcapi fetch failed",
			"track_id", track.ID,
			"title", track.Title,
			"artist", track.Artist,
			"error", err,
		)
		return Lyrics{}, ErrLyricsNotFound
	}

	s.onlineCache.store(track.ID, content)
	return Lyrics{
		TrackID:  track.ID,
		Encoding: "UTF-8",
		Source:   "lrcapi",
		Content:  content,
	}, nil
}

func readLyricsFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxLyricsFileBytes {
		return nil, ErrLyricsTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(file, maxLyricsFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxLyricsFileBytes {
		return nil, ErrLyricsTooLarge
	}
	return data, nil
}

func (s *LyricsService) readEmbeddedLyrics(ctx context.Context, track Track) (Lyrics, error) {
	if s.metadataReader == nil {
		return Lyrics{}, ErrLyricsNotFound
	}

	embedded, err := s.metadataReader.Read(ctx, track.Path)
	if err != nil {
		return Lyrics{}, ErrLyricsNotFound
	}
	if strings.TrimSpace(embedded.Lyrics.Content) == "" {
		return Lyrics{}, ErrLyricsNotFound
	}

	encodingName := embedded.Lyrics.Encoding
	if encodingName == "" {
		encodingName = "UTF-8"
	}
	source := embedded.Lyrics.Source
	if source == "" {
		source = "embedded"
	}

	return Lyrics{
		TrackID:  track.ID,
		Encoding: encodingName,
		Source:   source,
		Content:  embedded.Lyrics.Content,
	}, nil
}

func FindLyricsPath(trackPath string) (string, error) {
	basePath := stringsTrimExtension(trackPath)
	for _, extension := range lyricsCandidateFileExtension {
		candidate := basePath + extension
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", ErrLyricsNotFound
}

func DecodeLyrics(data []byte) (string, string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return string(data), "UTF-8", nil
	}

	if decoded, ok := decodeLegacyStrict(data, simplifiedchinese.GB18030); ok {
		return decoded, "GB18030", nil
	}
	if decoded, ok := decodeLegacyStrict(data, simplifiedchinese.GBK); ok {
		return decoded, "GBK", nil
	}

	return "", "", ErrUnsupportedLyricsEncoding
}

func decodeLegacyStrict(data []byte, codec encoding.Encoding) (string, bool) {
	decoded, _, err := transform.Bytes(codec.NewDecoder(), data)
	if err != nil || !utf8.Valid(decoded) {
		return "", false
	}

	encoded, _, err := transform.Bytes(codec.NewEncoder(), decoded)
	if err != nil || !bytes.Equal(encoded, data) {
		return "", false
	}

	return string(decoded), true
}

func stringsTrimExtension(path string) string {
	return path[:len(path)-len(filepath.Ext(path))]
}
