package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kayushkin/llm-bridge/msg"
	photostore "github.com/kayushkin/photo-store"
)

// modelChecker asks a model that can see whether a suggested face is the
// person suggested, by showing it the face beside faces already known to be
// them, and confirms or rejects the suggestion on its answer.
type modelChecker struct {
	web          client
	storeURL     string
	bridgeURL    string
	serviceToken string
	role         string
	faces        map[string]photostore.Face // id → face with its box
	thumbnails   map[string]image.Image
}

const modelCheckSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["same","different","unsure"]},"reason":{"type":"string"}},"required":["verdict","reason"]}`

const modelCheckSystemPrompt = `You compare faces in a private family photo archive, for its owner. ` +
	`The first image is one face cut from a photo. The others are faces already confirmed as one person. ` +
	`Say "same" only if the first face is clearly that person, "different" if it is clearly someone else, ` +
	`and "unsure" when the face is too small, blurred, turned away, or a different age makes it hard to tell. ` +
	`Judge by the face, not clothes or setting. Give a one-sentence reason.`

func (c *modelChecker) thumbnail(photoID string) (image.Image, error) {
	if cached, seen := c.thumbnails[photoID]; seen {
		return cached, nil
	}
	response, err := c.web.http.Get(c.storeURL + "/photos/" + url.PathEscape(photoID) + "/thumbnail")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thumbnail of %s: %d", photoID, response.StatusCode)
	}
	decoded, err := jpeg.Decode(response.Body)
	if err != nil {
		return nil, fmt.Errorf("thumbnail of %s: %w", photoID, err)
	}
	if len(c.thumbnails) > 200 {
		c.thumbnails = map[string]image.Image{}
	}
	c.thumbnails[photoID] = decoded
	return decoded, nil
}

// faceCrop cuts a face from its photo's thumbnail with some room around it,
// as a JPEG for the model.
func (c *modelChecker) faceCrop(faceID string) (msg.OneShotImage, error) {
	face, known := c.faces[faceID]
	if !known {
		return msg.OneShotImage{}, fmt.Errorf("no face %s", faceID)
	}
	picture, err := c.thumbnail(face.PhotoID)
	if err != nil {
		return msg.OneShotImage{}, err
	}
	bounds := picture.Bounds()
	width, height := float64(bounds.Dx()), float64(bounds.Dy())
	marginX := (face.Box.Right - face.Box.Left) * 0.3
	marginY := (face.Box.Bottom - face.Box.Top) * 0.3
	crop := image.Rect(int((face.Box.Left-marginX)*width), int((face.Box.Top-marginY)*height),
		int((face.Box.Right+marginX)*width), int((face.Box.Bottom+marginY)*height)).Intersect(bounds)
	if crop.Empty() {
		return msg.OneShotImage{}, fmt.Errorf("%s's box lies outside its thumbnail", faceID)
	}
	cropper, ok := picture.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return msg.OneShotImage{}, fmt.Errorf("thumbnail of %s cannot be cropped", face.PhotoID)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, cropper.SubImage(crop), &jpeg.Options{Quality: 88}); err != nil {
		return msg.OneShotImage{}, err
	}
	return msg.OneShotImage{MediaType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(encoded.Bytes())}, nil
}

type modelVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Model   string `json:"-"`
}

// ask shows the model one face and up to three known faces of a person.
func (c *modelChecker) ask(faceID string, knownFaceIDs []string, context string) (modelVerdict, error) {
	images := []msg.OneShotImage{}
	crop, err := c.faceCrop(faceID)
	if err != nil {
		return modelVerdict{}, err
	}
	images = append(images, crop)
	for _, known := range knownFaceIDs {
		crop, err := c.faceCrop(known)
		if err != nil {
			return modelVerdict{}, err
		}
		images = append(images, crop)
	}
	prompt := fmt.Sprintf("Image 1 is the face to judge. Images 2 to %d are the person it may be.", len(images))
	if context != "" {
		prompt += " What the archive knows about where the photo came from: " + context
	}
	body, err := json.Marshal(msg.OneShotRequest{Prompt: prompt, SystemPrompt: modelCheckSystemPrompt, ModelRole: c.role,
		Caller: matcherName, Schema: json.RawMessage(modelCheckSchema), MaxTokens: 400, Images: images})
	if err != nil {
		return modelVerdict{}, err
	}
	request, err := http.NewRequest(http.MethodPost, c.bridgeURL+"/oneshot", bytes.NewReader(body))
	if err != nil {
		return modelVerdict{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-LLM-Bridge-Service-Token", c.serviceToken)
	response, err := c.web.http.Do(request)
	if err != nil {
		return modelVerdict{}, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		return modelVerdict{}, err
	}
	if response.StatusCode != http.StatusOK {
		return modelVerdict{}, fmt.Errorf("POST /oneshot (role %s): %d %s", c.role, response.StatusCode, bytes.TrimSpace(answer))
	}
	var reply msg.OneShotResponse
	if err := json.Unmarshal(answer, &reply); err != nil {
		return modelVerdict{}, err
	}
	var verdict modelVerdict
	if err := json.Unmarshal(reply.Parsed, &verdict); err != nil {
		return modelVerdict{}, fmt.Errorf("the model's answer is not a verdict: %s", reply.Parsed)
	}
	verdict.Model = reply.Model
	return verdict, nil
}

// markdownToPlain drops the ref chips from evidence, leaving their text, for
// a prompt.
func markdownToPlain(markdown string) string {
	return personReference.ReplaceAllString(strings.NewReplacer("[", "", "]", "").Replace(markdown), "")
}

// checkSuggestions asks the model about up to limit suggested links with a
// face, and confirms the ones it says are the person and rejects the ones it
// says are not. A link to a person with no confirmed face is compared with
// the other faces suggested as them. It answers how many it confirmed,
// rejected and left.
func (c *modelChecker) checkSuggestions(links []photostore.PhotoPerson, limit int, dryRun bool) (map[string]int, error) {
	confirmedFaces := map[string][]string{}
	suggestedFaces := map[string][]string{}
	var suggestions []photostore.PhotoPerson
	for _, link := range links {
		if link.FaceID == "" {
			continue
		}
		switch link.Status {
		case photostore.PhotoPersonConfirmed:
			confirmedFaces[link.PersonID] = append(confirmedFaces[link.PersonID], link.FaceID)
		case photostore.PhotoPersonSuggested:
			suggestedFaces[link.PersonID] = append(suggestedFaces[link.PersonID], link.FaceID)
			suggestions = append(suggestions, link)
		}
	}
	// The surest suggestions first, so a capped run spends on the likeliest.
	sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].Confidence > suggestions[j].Confidence })
	outcome := map[string]int{}
	for _, link := range suggestions {
		if outcome["asked"] >= limit {
			break
		}
		known := confirmedFaces[link.PersonID]
		comparedWith := "faces confirmed as them"
		if len(known) == 0 {
			comparedWith = "other faces suggested as them"
			for _, faceID := range suggestedFaces[link.PersonID] {
				if faceID != link.FaceID {
					known = append(known, faceID)
				}
			}
		}
		if len(known) == 0 {
			continue
		}
		known = known[:min(3, len(known))]
		outcome["asked"]++
		verdict, err := c.ask(link.FaceID, known, markdownToPlain(link.Evidence))
		if err != nil {
			return outcome, fmt.Errorf("%s: %w", link.ID, err)
		}
		evidence := fmt.Sprintf("%s A model compared the face with %d %s and said %s: %s", link.Evidence, len(known), comparedWith, verdict.Verdict, verdict.Reason)
		var change photostore.PhotoPersonDecision
		decidedBy := fmt.Sprintf("%s: face match and %s agree", matcherName, verdict.Model)
		switch verdict.Verdict {
		case "same":
			status := photostore.PhotoPersonConfirmed
			change = photostore.PhotoPersonDecision{Status: &status, DecidedBy: &decidedBy, Evidence: &evidence}
		case "different":
			status := photostore.PhotoPersonRejected
			decidedBy = fmt.Sprintf("%s: %s says this is someone else", matcherName, verdict.Model)
			change = photostore.PhotoPersonDecision{Status: &status, DecidedBy: &decidedBy, Evidence: &evidence}
		default:
			change = photostore.PhotoPersonDecision{Evidence: &evidence}
		}
		outcome[verdict.Verdict]++
		if dryRun {
			fmt.Printf("%s %s %s: %s (%s)\n", link.ID, link.PhotoID, link.PersonID, verdict.Verdict, verdict.Reason)
			continue
		}
		if err := c.patch(link.ID, change); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

func (c *modelChecker) patch(id string, change photostore.PhotoPersonDecision) error {
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPatch, c.storeURL+"/photo-people/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.web.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		answer, _ := io.ReadAll(response.Body)
		return fmt.Errorf("PATCH /photo-people/%s: %d %s", id, response.StatusCode, bytes.TrimSpace(answer))
	}
	return nil
}

// loadFaceBoxes reads every face's box, which the crops need.
func (c *modelChecker) loadFaceBoxes(model string) error {
	after := ""
	for {
		query := url.Values{"model": {model}, "limit": {"5000"}}
		if after != "" {
			query.Set("after", after)
		}
		var page []photostore.Face
		if err := c.web.getJSON(c.storeURL+"/faces?"+query.Encode(), nil, &page); err != nil {
			return fmt.Errorf("faces: %w", err)
		}
		for _, face := range page {
			c.faces[face.ID] = face
		}
		if len(page) < 5000 {
			return nil
		}
		after = page[len(page)-1].ID
	}
}
