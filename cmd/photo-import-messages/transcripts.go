package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// directMessagesFolder is the folder multichat-fetch-discord-history writes
// DMs to, in place of a server id.
const directMessagesFolder = "direct-messages"

// The journal-store tags its importers give Discord and Messenger
// transcripts.
const (
	discordTranscriptTag   = "discord-package"
	messengerTranscriptTag = "facebook-messenger"
)

// discordEpochMilliseconds is when Discord's snowflake ids start counting.
const discordEpochMilliseconds = 1420070400000

// facebookMessageFolders are the folders under a Facebook export's messages/
// that hold threads.
var facebookMessageFolders = []string{"inbox", "archived_threads", "filtered_threads", "message_requests", "e2ee_cutover"}

type journalStore struct {
	base   string
	client *http.Client
}

type transcript struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// transcriptCiting finds the one transcript with tag whose text holds token,
// a Discord message id or a file name.
func (journal journalStore) transcriptCiting(tag, token string) (transcript, error) {
	query := url.Values{"tag": {tag}, "q": {`"` + token + `"`}, "limit": {"20"}}
	response, err := journal.client.Get(journal.base + "/entries?" + query.Encode())
	if err != nil {
		return transcript{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return transcript{}, err
	}
	if response.StatusCode != http.StatusOK {
		return transcript{}, fmt.Errorf("journal-store answered %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var found []transcript
	if err := json.Unmarshal(body, &found); err != nil {
		return transcript{}, err
	}
	var matches []transcript
	for _, entry := range found {
		if strings.Contains(entry.Body, token) {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		return transcript{}, errNotOneTranscript{count: len(matches)}
	}
	return matches[0], nil
}

type errNotOneTranscript struct{ count int }

func (e errNotOneTranscript) Error() string {
	return fmt.Sprintf("%d transcripts cite it", e.count)
}

// chatName is a transcript title's chat: "Discord: rexdawn7, 2026-04-05"
// gives "rexdawn7".
func chatName(title string) string {
	_, chat, _ := strings.Cut(title, ": ")
	if comma := strings.LastIndex(chat, ", "); comma >= 0 {
		chat = chat[:comma]
	}
	return chat
}

// importDirectMessages adds the images multichat-fetch-discord-history saved
// from DMs, each cited by the Discord transcript that holds its message.
func importDirectMessages(journal journalStore, in *importer, folder string) error {
	manifest := filepath.Join(folder, "images.jsonl")
	file, err := os.Open(manifest)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line struct {
			MessageID   string `json:"message_id"`
			ContentType string `json:"content_type"`
			Path        string `json:"path"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("%s: %w", manifest, err)
		}
		snowflake, err := strconv.ParseInt(line.MessageID, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: message id %q: %w", manifest, line.MessageID, err)
		}
		found, err := journal.transcriptCiting(discordTranscriptTag, line.MessageID)
		if _, notOne := err.(errNotOneTranscript); notOne {
			in.report["no one Discord transcript holds its message ("+err.Error()+")"]++
			continue
		}
		if err != nil {
			return err
		}
		original, err := os.ReadFile(filepath.Join(folder, line.Path))
		if err != nil {
			return err
		}
		sentAt := time.UnixMilli(snowflake>>22 + discordEpochMilliseconds)
		in.add(original, line.ContentType, found.ID, "discord/direct-messages/"+chatName(found.Title), sentAt)
	}
	return scanner.Err()
}

// importFacebookMessages adds the photos in a Facebook export's Messenger
// threads of at most maxMembers people, each cited by the Messenger
// transcript that names its file.
func importFacebookMessages(journal journalStore, in *importer, messagesFolder string, maxMembers int) error {
	exportRoot := filepath.Dir(filepath.Dir(messagesFolder))
	for _, folder := range facebookMessageFolders {
		files, err := filepath.Glob(filepath.Join(messagesFolder, folder, "*", "message_*.json"))
		if err != nil {
			return err
		}
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var thread struct {
				Participants []json.RawMessage `json:"participants"`
				Messages     []struct {
					TimestampMs int64 `json:"timestamp_ms"`
					Photos      []struct {
						URI string `json:"uri"`
					} `json:"photos"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(raw, &thread); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			if len(thread.Participants) > maxMembers {
				continue
			}
			for _, message := range thread.Messages {
				for _, photo := range message.Photos {
					name := path.Base(photo.URI)
					found, err := journal.transcriptCiting(messengerTranscriptTag, name)
					if _, notOne := err.(errNotOneTranscript); notOne {
						in.report["no one Messenger transcript names its file ("+err.Error()+")"]++
						continue
					}
					if err != nil {
						return err
					}
					original, err := os.ReadFile(filepath.Join(exportRoot, filepath.FromSlash(photo.URI)))
					if os.IsNotExist(err) {
						in.report["the export lacks the file"]++
						continue
					}
					if err != nil {
						return err
					}
					in.add(original, mediaTypeOf(name), found.ID, "messages/facebook/"+chatName(found.Title), time.UnixMilli(message.TimestampMs))
				}
			}
		}
	}
	return nil
}

// mediaTypeOf is a photo's type from its file name; the export records none.
func mediaTypeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
