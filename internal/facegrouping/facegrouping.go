// Package facegrouping gathers faces nobody has named into groups that are
// each one person, so a person can name a whole group at once. It decides
// only; reading the store and writing the groups is the command's job.
//
// A face joins the group whose average face it is closest to, never the
// group of whichever single face it happens to resemble: linking face to
// nearest face chains one person into the next through a few in-between
// faces, and in a first measurement on real photos that put two people in
// one group of thousands. Two faces on one photo are two people, so a group
// never takes a second face from a photo it already holds.
package facegrouping

import (
	"math"
	"sort"

	"github.com/kayushkin/photo-store/internal/peoplematching"
)

// Settings tune the grouping. DefaultSettings holds the values in use.
type Settings struct {
	// JoinSimilarity is how close a face must be to a group's average face
	// to join it.
	JoinSimilarity float64
	// MinimumMargin is how far the best open group must lead the next one
	// for a face to join an existing group.
	MinimumMargin float64
	// MinimumPhotos is the fewest photos a new group must span to be kept;
	// a face seen once or twice waits for more.
	MinimumPhotos int
}

// DefaultSettings: buffalo_l faces of one person usually score above 0.5
// against each other and of two people below 0.3; against an average face,
// which is cleaner than any one face, one person's score higher still.
var DefaultSettings = Settings{JoinSimilarity: 0.5, MinimumMargin: 0.05, MinimumPhotos: 3}

// Face is a face to group, with how sure the finder was it is a face.
type Face struct {
	peoplematching.Face
	DetectionScore float64
}

// Group is a set of faces, with each face's similarity to its average.
type Group struct {
	// ID is the store's face_group id; "" for a group made here.
	ID      string
	Members []Member
}

// Member is one face of a group.
type Member struct {
	Face       Face
	Similarity float64
}

// Result is what Assign decided.
type Result struct {
	// Joined maps an existing group's id to the faces that join it.
	Joined map[string][]Member
	// Created are new groups, the most photos first.
	Created []Group
}

type cluster struct {
	id     string
	sum    []float64
	photos map[string]bool
	faces  []Face
}

func newCluster(id string, dimensions int) *cluster {
	return &cluster{id: id, sum: make([]float64, dimensions), photos: map[string]bool{}}
}

func (c *cluster) add(face Face) {
	for index, value := range face.Embedding {
		c.sum[index] += float64(value)
	}
	c.photos[face.PhotoID] = true
	c.faces = append(c.faces, face)
}

// average is the normalized mean of the cluster's faces.
func (c *cluster) average() []float32 {
	var squares float64
	for _, value := range c.sum {
		squares += value * value
	}
	length := math.Sqrt(squares)
	averaged := make([]float32, len(c.sum))
	if length == 0 {
		return averaged
	}
	for index, value := range c.sum {
		averaged[index] = float32(value / length)
	}
	return averaged
}

// Assign places free faces: first into the open groups they clearly belong
// to, then the rest into new groups of their own, keeping only new groups
// that span settings.MinimumPhotos photos.
func Assign(free []Face, open []Group, settings Settings) Result {
	result := Result{Joined: map[string][]Member{}}
	if len(free) == 0 {
		return result
	}
	dimensions := len(free[0].Embedding)
	existing := make([]*cluster, 0, len(open))
	for _, group := range open {
		each := newCluster(group.ID, dimensions)
		for _, member := range group.Members {
			each.add(member.Face)
		}
		existing = append(existing, each)
	}
	averages := make([][]float32, len(existing))
	for index, each := range existing {
		averages[index] = each.average()
	}
	// Surest faces first, so a group's average forms from clear faces.
	ordered := append([]Face(nil), free...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].DetectionScore > ordered[j].DetectionScore })

	left := []Face{}
	for _, face := range ordered {
		best, second, bestIndex := -1.0, -1.0, -1
		for index, each := range existing {
			if each.photos[face.PhotoID] {
				continue
			}
			similarity := peoplematching.Similarity(face.Embedding, averages[index])
			if similarity > best {
				best, second, bestIndex = similarity, best, index
			} else if similarity > second {
				second = similarity
			}
		}
		if bestIndex >= 0 && best >= settings.JoinSimilarity && best-second >= settings.MinimumMargin {
			existing[bestIndex].photos[face.PhotoID] = true
			result.Joined[existing[bestIndex].id] = append(result.Joined[existing[bestIndex].id], Member{Face: face, Similarity: best})
			continue
		}
		left = append(left, face)
	}

	// New groups: each face joins the closest new group's average when close
	// enough, or starts one. A second pass moves each face to the average it
	// is now closest to, since the averages settled after it was placed.
	clusters := []*cluster{}
	place := func(faces []Face, clusters []*cluster, averages [][]float32) []*cluster {
		for _, face := range faces {
			best, bestIndex := -1.0, -1
			for index, each := range clusters {
				if each.photos[face.PhotoID] {
					continue
				}
				average := averages[index]
				if average == nil {
					average = each.average()
				}
				if similarity := peoplematching.Similarity(face.Embedding, average); similarity > best {
					best, bestIndex = similarity, index
				}
			}
			if bestIndex >= 0 && best >= settings.JoinSimilarity {
				clusters[bestIndex].add(face)
				continue
			}
			fresh := newCluster("", dimensions)
			fresh.add(face)
			clusters = append(clusters, fresh)
			averages = append(averages, nil)
		}
		return clusters
	}
	clusters = place(left, clusters, make([][]float32, 0))
	settled := make([][]float32, len(clusters))
	for index, each := range clusters {
		settled[index] = each.average()
	}
	second := make([]*cluster, len(clusters))
	for index := range clusters {
		second[index] = newCluster("", dimensions)
	}
	secondAverages := make([][]float32, len(clusters))
	copy(secondAverages, settled)
	second = place(left, second, secondAverages)

	for _, each := range second {
		if len(each.photos) < settings.MinimumPhotos {
			continue
		}
		average := each.average()
		group := Group{}
		for _, face := range each.faces {
			group.Members = append(group.Members, Member{Face: face, Similarity: peoplematching.Similarity(face.Embedding, average)})
		}
		sort.SliceStable(group.Members, func(i, j int) bool { return group.Members[i].Similarity > group.Members[j].Similarity })
		result.Created = append(result.Created, group)
	}
	sort.SliceStable(result.Created, func(i, j int) bool { return len(result.Created[i].Members) > len(result.Created[j].Members) })
	return result
}
