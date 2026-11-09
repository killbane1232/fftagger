package model

type Tags struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"album_artist"`
	Date        string `json:"date"`
	Track       string `json:"track"`
	Disc        string `json:"disc"`
	Genre       string `json:"genre"`
	Composer    string `json:"composer"`
	Comment     string `json:"comment"`
}

func (t Tags) Map() map[string]string {
	return map[string]string{"title": t.Title, "artist": t.Artist, "album": t.Album, "album_artist": t.AlbumArtist, "date": t.Date, "track": t.Track, "disc": t.Disc, "genre": t.Genre, "composer": t.Composer, "comment": t.Comment}
}

type Track struct {
	ID            int64             `json:"id"`
	Path          string            `json:"path"`
	Name          string            `json:"name"`
	Tags          Tags              `json:"tags"`
	RawTags       map[string]string `json:"raw_tags"`
	Format        string            `json:"format"`
	Codec         string            `json:"codec"`
	Duration      float64           `json:"duration"`
	SampleRate    int               `json:"sample_rate"`
	Channels      int               `json:"channels"`
	Size          int64             `json:"size"`
	ModTime       int64             `json:"mod_time,string"`
	HasCover      bool              `json:"has_cover"`
	Writable      bool              `json:"writable"`
	CoverWritable bool              `json:"cover_writable"`
	Cover         []byte            `json:"-"`
}
