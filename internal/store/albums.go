package store

import (
	"fftagger/internal/model"
	"strings"
)

type AlbumPeer struct {
	Track        model.Track
	CoverMatches bool
}

// Match exact album/artist keys (Unicode case folding performed in Go), never
// prefix searches. Read artwork equality in SQLite without loading every image.
func (s *Store) AlbumPeers(artist, album string, exclude int64, cover []byte) ([]AlbumPeer, error) {
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(album) == "" {
		return nil, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	rows, err := s.reader.query(`SELECT id,document,cover=? FROM tracks WHERE artist_key=? AND album_key=? AND id<>? ORDER BY path,id`, cover, strings.ToLower(artist), strings.ToLower(album), exclude)
	if err != nil {
		return nil, err
	}
	peers := make([]AlbumPeer, 0, len(rows))
	for _, r := range rows {
		track, err := decode(r)
		if err != nil {
			return nil, err
		}
		peers = append(peers, AlbumPeer{Track: track, CoverMatches: r[2].(int64) != 0})
	}
	return peers, nil
}
