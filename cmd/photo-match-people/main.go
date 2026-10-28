// Command photo-match-people proposes who is in each photo. It reads every
// face photo-store holds, the people already confirmed on photos, and each
// photo's context — the chat a photo was sent in (multichat's room members,
// or the sender in a big channel) and the people a transcript citing it links
// — and writes links through POST /photo-people:
//
//   - confirmed, when a face is close to a person's confirmed faces, clearly
//     closer than to anyone else, and the context names that person;
//   - suggested, when a face is close but the context does not name them, or
//     when a face recurs across one person's chat photos (how a person with
//     no confirmed photo gets a first set).
//
// It never changes a confirmed or rejected link; photo-store refuses that.
// -seed-person with -seed-photos confirms, on photos a person said they are
// in, the face that recurs across them, so matching has a start.
//
//	MULTICHAT_TOKEN=… photo-match-people -operator person_000001 -dry-run
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kayushkin/llm-bridge/servicesettings"
	photostore "github.com/kayushkin/photo-store"
	"github.com/kayushkin/photo-store/internal/peoplematching"
)

const matcherName = "photo-match-people"

// Faces smaller than this share of the thumbnail's width, or found with less
// certainty, are too unsure to match.
const (
	minimumFaceWidth      = 0.035
	minimumDetectionScore = 0.6
	// largestChatForContext is the most members a room may have for all of
	// them to count as context; a bigger room counts only its sender.
	largestChatForContext = 8
)

var personReference = regexp.MustCompile(`ref:(person_[0-9]{6,})`)

type client struct {
	http *http.Client
}

func (c client) getJSON(target string, headers map[string]string, out any) error {
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", target, response.StatusCode, bytes.TrimSpace(body))
	}
	return json.Unmarshal(body, out)
}

var errNotFound = fmt.Errorf("not found")

func (c client) postJSON(target string, payload any) (int, []byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	response, err := c.http.Post(target, "application/json", bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response.StatusCode, body, err
}

type settings struct {
	storeURL, multichatURL, multichatToken, journalURL, peopleURL string
}

// contextReader finds the people a photo's source names, caching each look-up.
type contextReader struct {
	client
	settings
	rooms      map[string]room
	persons    map[string]string // matrix user id → person id, "" for nobody
	entries    map[string][]string
	operatorID string
}

type room struct {
	MemberCount int `json:"member_count"`
	Members     []struct {
		UserID string `json:"user_id"`
	} `json:"members"`
	RoomID string `json:"room_id"`
}

func (r *contextReader) loadRooms() error {
	var rooms []room
	if err := r.getJSON(r.multichatURL+"/api/conversations", map[string]string{"Authorization": "Bearer " + r.multichatToken}, &rooms); err != nil {
		return fmt.Errorf("multichat conversations: %w", err)
	}
	r.rooms = map[string]room{}
	for _, each := range rooms {
		r.rooms[each.RoomID] = each
	}
	return nil
}

func (r *contextReader) personOfUser(userID string) (string, error) {
	if personID, known := r.persons[userID]; known {
		return personID, nil
	}
	var answer struct {
		Person struct {
			ID string `json:"id"`
		} `json:"person"`
	}
	err := r.getJSON(r.peopleURL+"/identities/resolve?"+url.Values{"kind": {"matrix_user"}, "value": {userID}}.Encode(), nil, &answer)
	if err != nil && err != errNotFound {
		return "", fmt.Errorf("people-store resolve %s: %w", userID, err)
	}
	r.persons[userID] = answer.Person.ID
	return answer.Person.ID, nil
}

// peopleOf answers the people a photo's source names, with a short reason
// in markdown for the evidence.
func (r *contextReader) peopleOf(photo photostore.Photo) (map[string]string, error) {
	named := map[string]string{}
	switch {
	case strings.HasPrefix(photo.SourcePath, "message_"):
		var answer struct {
			Messages []struct {
				RoomID       string `json:"room_id"`
				SenderUserID string `json:"sender_user_id"`
				RoomName     string `json:"room_name"`
			} `json:"messages"`
		}
		err := r.getJSON(r.multichatURL+"/api/message-log/"+url.PathEscape(photo.SourcePath), map[string]string{"Authorization": "Bearer " + r.multichatToken}, &answer)
		if err == errNotFound || (err == nil && len(answer.Messages) == 0) {
			return named, nil
		}
		if err != nil {
			return nil, fmt.Errorf("multichat %s: %w", photo.SourcePath, err)
		}
		message := answer.Messages[0]
		users := []string{message.SenderUserID}
		reason := fmt.Sprintf("sent in [%s](ref:%s)", message.RoomName, photo.SourcePath)
		if each, known := r.rooms[message.RoomID]; known && each.MemberCount <= largestChatForContext {
			users = users[:0]
			for _, member := range each.Members {
				users = append(users, member.UserID)
			}
			// The operator is in every small chat of their own, under
			// whichever handle that app gives them.
			if r.operatorID != "" {
				named[r.operatorID] = reason
			}
		} else {
			reason = fmt.Sprintf("posted in [%s](ref:%s)", message.RoomName, photo.SourcePath)
		}
		for _, userID := range users {
			personID, err := r.personOfUser(userID)
			if err != nil {
				return nil, err
			}
			if personID != "" {
				named[personID] = reason
			}
		}
	case strings.HasPrefix(photo.SourcePath, "entry_"):
		people, cached := r.entries[photo.SourcePath]
		if !cached {
			var entry struct {
				Body string `json:"body"`
			}
			if err := r.getJSON(r.journalURL+"/entries/"+url.PathEscape(photo.SourcePath), nil, &entry); err != nil && err != errNotFound {
				return nil, fmt.Errorf("journal-store %s: %w", photo.SourcePath, err)
			}
			for _, match := range personReference.FindAllStringSubmatch(entry.Body, -1) {
				people = append(people, match[1])
			}
			r.entries[photo.SourcePath] = people
		}
		for _, personID := range people {
			named[personID] = fmt.Sprintf("in [the day's chat](ref:%s)", photo.SourcePath)
		}
		// The operator is in every chat of their own.
		if len(people) > 0 && r.operatorID != "" {
			named[r.operatorID] = fmt.Sprintf("in [the day's chat](ref:%s)", photo.SourcePath)
		}
	}
	return named, nil
}

func main() {
	operator := flag.String("operator", "", "the person_ id whose photos these are, who is in every chat")
	seedPerson := flag.String("seed-person", "", "a person_ id to confirm on -seed-photos")
	seedPhotos := flag.String("seed-photos", "", "comma-separated photo ids the -seed-person said they are in")
	seedDecidedBy := flag.String("seed-decided-by", "", "who said so, recorded as decided_by")
	model := flag.String("model", "insightface-buffalo_l", "the face model whose faces to match")
	storeURL := flag.String("store-url", "http://"+photostore.DefaultListenAddress, "photo-store's base URL")
	dryRun := flag.Bool("dry-run", false, "decide and report, write nothing")
	modelCheck := flag.Int("model-check", 0, "ask a model that can see about up to this many suggestions after matching, confirming or rejecting them; 0 = none")
	modelRole := flag.String("model-role", "default", "the model-store role the model check asks; its models must see images")
	flag.Parse()
	registry, err := photostore.NewMatchPeopleSettingsRegistry(servicesettings.ProcessEnvironment())
	if err == nil {
		err = registry.CheckRequired()
	}
	if err != nil {
		fail(err)
	}
	config := settings{storeURL: strings.TrimRight(*storeURL, "/"),
		multichatURL: strings.TrimRight(registry.String(photostore.SettingMultichatURL), "/"), multichatToken: registry.String(photostore.SettingMultichatToken),
		journalURL: strings.TrimRight(registry.String(photostore.SettingJournalStoreURL), "/"), peopleURL: strings.TrimRight(registry.String(photostore.SettingPeopleStoreURL), "/")}
	web := client{http: &http.Client{Timeout: 2 * time.Minute}}
	if err := run(web, config, *operator, *model, *seedPerson, *seedPhotos, *seedDecidedBy, *dryRun); err != nil {
		fail(err)
	}
	if *modelCheck > 0 {
		token := registry.String(photostore.SettingBridgeServiceToken)
		if token == "" {
			fail(fmt.Errorf("-model-check needs LLMBRIDGE_SERVICE_TOKEN: POST /oneshot answers nothing without it"))
		}
		checker := &modelChecker{web: web, storeURL: config.storeURL, bridgeURL: strings.TrimRight(registry.String(photostore.SettingBridgeURL), "/"),
			serviceToken: token, role: *modelRole, faces: map[string]photostore.Face{}, embeddings: map[string][]float32{},
			years: map[string]string{}, thumbnails: map[string]image.Image{}}
		if err := checker.loadFaceBoxes(*model); err != nil {
			fail(err)
		}
		links, err := loadLinks(web, config.storeURL)
		if err != nil {
			fail(err)
		}
		outcome, err := checker.checkSuggestions(links, *modelCheck, *dryRun)
		fmt.Printf("model check: asked %d; same %d, different %d, unsure %d, failed %d; %d wait for a first confirmed face\n", outcome["asked"], outcome["same"], outcome["different"], outcome["unsure"], outcome["failed"], outcome["waiting"])
		if err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", matcherName, err)
	os.Exit(1)
}

func run(web client, config settings, operator, model, seedPerson, seedPhotos, seedDecidedBy string, dryRun bool) error {
	faces, err := loadFaces(web, config.storeURL, model)
	if err != nil {
		return err
	}
	facesByPhoto := map[string][]peoplematching.Face{}
	faceByID := map[string]peoplematching.Face{}
	for _, face := range faces {
		facesByPhoto[face.PhotoID] = append(facesByPhoto[face.PhotoID], face)
		faceByID[face.ID] = face
	}
	links, err := loadLinks(web, config.storeURL)
	if err != nil {
		return err
	}
	linked := map[string]photostore.PhotoPerson{} // photo|person
	for _, link := range links {
		linked[link.PhotoID+"|"+link.PersonID] = link
	}
	written := map[string]int{}
	propose := func(proposal photostore.PhotoPersonProposal) error {
		if existing, seen := linked[proposal.PhotoID+"|"+proposal.PersonID]; seen && (existing.Status != photostore.PhotoPersonSuggested || proposal.Status == photostore.PhotoPersonSuggested) {
			return nil
		}
		written[proposal.Status]++
		linked[proposal.PhotoID+"|"+proposal.PersonID] = photostore.PhotoPerson{PhotoID: proposal.PhotoID, PersonID: proposal.PersonID, FaceID: proposal.FaceID, Status: proposal.Status}
		if dryRun {
			return nil
		}
		status, body, err := web.postJSON(config.storeURL+"/photo-people", proposal)
		if err != nil {
			return err
		}
		if status != http.StatusOK && status != http.StatusCreated {
			return fmt.Errorf("POST /photo-people %s %s: %d %s", proposal.PhotoID, proposal.PersonID, status, bytes.TrimSpace(body))
		}
		return nil
	}

	if seedPerson != "" {
		if seedDecidedBy == "" {
			return fmt.Errorf("-seed-person needs -seed-decided-by: who said these photos are them")
		}
		chosen := map[string][]peoplematching.Face{}
		for _, photoID := range strings.Split(seedPhotos, ",") {
			if photoFaces := facesByPhoto[strings.TrimSpace(photoID)]; len(photoFaces) > 0 {
				chosen[strings.TrimSpace(photoID)] = photoFaces
			}
		}
		for _, seed := range peoplematching.SeedFaces(chosen) {
			if err := propose(photostore.PhotoPersonProposal{PhotoID: seed.PhotoID, PersonID: seedPerson, FaceID: seed.ID, Status: photostore.PhotoPersonConfirmed,
				Confidence: 1, Evidence: "The person said this photo is them; this face recurs across the photos they named.", ProposedBy: matcherName, DecidedBy: seedDecidedBy}); err != nil {
				return err
			}
		}
	}

	photos, err := loadPhotos(web, config.storeURL)
	if err != nil {
		return err
	}
	reader := &contextReader{client: web, settings: config, persons: map[string]string{}, entries: map[string][]string{}, operatorID: operator}
	if err := reader.loadRooms(); err != nil {
		return err
	}
	contextOf := map[string]map[string]string{}
	for _, photo := range photos {
		if len(facesByPhoto[photo.ID]) == 0 {
			continue
		}
		named, err := reader.peopleOf(photo)
		if err != nil {
			return err
		}
		contextOf[photo.ID] = named
	}

	references := func() map[string][]peoplematching.Face {
		byPerson := map[string][]peoplematching.Face{}
		for _, link := range linked {
			if link.Status == photostore.PhotoPersonConfirmed && link.FaceID != "" {
				if face, known := faceByID[link.FaceID]; known {
					byPerson[link.PersonID] = append(byPerson[link.PersonID], face)
				}
			}
		}
		return byPerson
	}

	// First: match every face against the people already confirmed.
	known := references()
	for _, face := range faces {
		context := map[string]bool{}
		for personID := range contextOf[face.PhotoID] {
			context[personID] = true
		}
		verdict := peoplematching.Decide(peoplematching.ScorePeople(face, known), context)
		if verdict.Status == "" {
			continue
		}
		evidence := fmt.Sprintf("Face matches %d confirmed photos of [them](ref:%s) (similarity %.2f).", len(known[verdict.PersonID]), verdict.PersonID, verdict.Similarity)
		if reason, named := contextOf[face.PhotoID][verdict.PersonID]; named {
			evidence += " The photo was " + reason + ", which names them."
		}
		proposal := photostore.PhotoPersonProposal{PhotoID: face.PhotoID, PersonID: verdict.PersonID, FaceID: face.ID, Status: verdict.Status,
			Confidence: verdict.Similarity, Evidence: evidence, ProposedBy: matcherName}
		if verdict.Status == photostore.PhotoPersonConfirmed {
			proposal.DecidedBy = matcherName + ": face match and context agree"
		}
		if err := propose(proposal); err != nil {
			return err
		}
	}

	// Then: for each person the context names who has no confirmed face, the
	// face that recurs across their one-to-one photos, leaving out faces of
	// people already known and faces already linked to anyone.
	// Only photos whose context names one person besides the operator: in a
	// group chat the face that recurs is one member's, and nothing here says
	// whose, so a cluster there would be suggested as every member.
	photosOfPerson := map[string][]string{}
	for photoID, named := range contextOf {
		var others []string
		for personID := range named {
			if personID != operator {
				others = append(others, personID)
			}
		}
		if len(others) == 1 {
			photosOfPerson[others[0]] = append(photosOfPerson[others[0]], photoID)
		}
	}
	// A face already linked to someone, any status, is not offered again.
	claimedFaces := map[string]bool{}
	for _, link := range linked {
		if link.FaceID != "" {
			claimedFaces[link.FaceID] = true
		}
	}
	people := make([]string, 0, len(photosOfPerson))
	for personID := range photosOfPerson {
		people = append(people, personID)
	}
	sort.Strings(people)
	for _, personID := range people {
		if len(known[personID]) > 0 {
			continue
		}
		var candidates []peoplematching.Face
		for _, photoID := range photosOfPerson[personID] {
			for _, face := range facesByPhoto[photoID] {
				if claimedFaces[face.ID] {
					continue
				}
				if scores := peoplematching.ScorePeople(face, known); len(scores) > 0 && scores[0].Similarity >= peoplematching.SuggestSimilarity {
					continue
				}
				candidates = append(candidates, face)
			}
		}
		cluster := peoplematching.ClusterInContext(candidates)
		for _, face := range cluster {
			claimedFaces[face.ID] = true
			if err := propose(photostore.PhotoPersonProposal{PhotoID: face.PhotoID, PersonID: personID, FaceID: face.ID, Status: photostore.PhotoPersonSuggested,
				Confidence: 0.5, ProposedBy: matcherName,
				Evidence: fmt.Sprintf("The same face recurs on %d photos whose chats name [them](ref:%s); this photo was %s.", len(cluster), personID, contextOf[face.PhotoID][personID])}); err != nil {
				return err
			}
		}
	}
	mode := "wrote"
	if dryRun {
		mode = "would write"
	}
	fmt.Printf("%d faces on %d photos; %d photos with context; %s %d confirmed and %d suggested links\n",
		len(faces), len(facesByPhoto), len(contextOf), mode, written[photostore.PhotoPersonConfirmed], written[photostore.PhotoPersonSuggested])
	return nil
}

func loadFaces(web client, storeURL, model string) ([]peoplematching.Face, error) {
	var faces []peoplematching.Face
	after := ""
	for {
		query := url.Values{"model": {model}, "include_embeddings": {"true"}, "limit": {"5000"}}
		if after != "" {
			query.Set("after", after)
		}
		var page []photostore.Face
		if err := web.getJSON(storeURL+"/faces?"+query.Encode(), nil, &page); err != nil {
			return nil, fmt.Errorf("faces: %w", err)
		}
		for _, face := range page {
			if face.DetectionScore < minimumDetectionScore || face.Box.Right-face.Box.Left < minimumFaceWidth {
				continue
			}
			embedding, err := peoplematching.DecodeEmbedding(face.EmbeddingFloat32Base64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", face.ID, err)
			}
			faces = append(faces, peoplematching.Face{ID: face.ID, PhotoID: face.PhotoID, Embedding: embedding})
		}
		if len(page) < 5000 {
			return faces, nil
		}
		after = page[len(page)-1].ID
	}
}

func loadLinks(web client, storeURL string) ([]photostore.PhotoPerson, error) {
	var links []photostore.PhotoPerson
	for offset := 0; ; offset += 5000 {
		var page []photostore.PhotoPerson
		if err := web.getJSON(fmt.Sprintf("%s/photo-people?limit=5000&offset=%d", storeURL, offset), nil, &page); err != nil {
			return nil, fmt.Errorf("photo-people: %w", err)
		}
		links = append(links, page...)
		if len(page) < 5000 {
			return links, nil
		}
	}
}

func loadPhotos(web client, storeURL string) ([]photostore.Photo, error) {
	var photos []photostore.Photo
	for offset := 0; ; offset += 1000 {
		var page []photostore.Photo
		if err := web.getJSON(fmt.Sprintf("%s/photos?limit=1000&offset=%d", storeURL, offset), nil, &page); err != nil {
			return nil, fmt.Errorf("photos: %w", err)
		}
		photos = append(photos, page...)
		if len(page) < 1000 {
			return photos, nil
		}
	}
}
