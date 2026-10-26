package photostore

// Where a photo's taken_at came from, most trusted first. Served at
// GET /date-sources.
const (
	DateFromExif     = "exif"     // the camera's own DateTimeOriginal
	DateFromFilename = "filename" // a date in the file's name (IMG_20190704_153000)
	DateFromMtime    = "mtime"    // the file's modification time: a copy or backup resets it, so it is weak
	// DateFromMessage is when the photo was sent in a chat: it was taken then
	// or before.
	DateFromMessage = "message"
	DateUnknown     = "unknown"
)

// DateSources is every date source a photo can have.
var DateSources = []string{DateFromExif, DateFromFilename, DateFromMtime, DateFromMessage, DateUnknown}

func knownDateSource(source string) bool {
	for _, known := range DateSources {
		if source == known {
			return true
		}
	}
	return false
}
