package main

import (
	"math/rand"
	"time"
)

// SimKnife is a swiss army knife of simulation
type SimKnife struct {
	rand *rand.Rand
}

type Event struct {
	ID     string
	Weight int
}

func NewSimKnife() *SimKnife {
	return &SimKnife{rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func NewSimKnifeUsingSeed(seed int64) *SimKnife {
	return &SimKnife{rand: rand.New(rand.NewSource(seed))}
}

func (s *SimKnife) randomWeightedSelect(events []Event) Event {
	var totalWeight int
	for _, e := range events {
		totalWeight += e.Weight
	}

	r := s.rand.Intn(totalWeight)
	for _, e := range events {
		r = r - e.Weight
		if r <= 0 {
			return e
		}
	}

	return events[0] // will obviously fail if there are no events...
}

// return a random string of the given length from a standard lowercase-only alphanumeric alphabet)
func (s *SimKnife) randomString(length int) string {
	return s.randomStringUsingCustomAlphabet(length, []rune("0123456789abcdefghijklmnopqrstuvwxyz"))
}

// return a random string of the given length from a custom alphabet.
func (s *SimKnife) randomStringUsingCustomAlphabet(length int, alphabet []rune) string {
	b := make([]rune, length)
	for i := range b {
		b[i] = alphabet[s.rand.Intn(len(alphabet))]
	}

	return string(b)
}
