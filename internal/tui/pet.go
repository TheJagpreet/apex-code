package tui

import (
	"math/rand"
	"strings"

	"github.com/charmbracelet/harmonica"
)

// petMood is one behavior/emotion the footer companion can be in.
type petMood int

const (
	petWalking petMood = iota
	petSitting
	petSleeping
	petWaking
	petJumping
	petPeeking
	petGrooming
	petPlaying
	petCurious
	petHappy
)

// petWidth is the reserved cell budget for the pet glyph plus a small emote.
const petWidth = 8

type petPersona struct {
	Name     string
	IdleA    string
	IdleB    string
	Sleeping string
	Waking   string
	Grooming string
	Curious  string
	HappyA   string
	HappyB   string
	Jumping  string
	PeekingR string
	PeekingL string
	PlayingR string
	PlayingL string
	WalkingR string
	WalkingL string
}

var petPersonas = []petPersona{
	{Name: "cat", IdleA: "🐱", IdleB: "😺", Sleeping: "😴", Waking: "🐱 ?", Grooming: "😽 ✨", Curious: "😼 ?", HappyA: "😸 ♪", HappyB: "😺 ♥", Jumping: "🐱 ⤴", PeekingR: "🐱›", PeekingL: "‹🐱", PlayingR: "😺", PlayingL: "😺", WalkingR: "🐱 →", WalkingL: "← 🐱"},
	{Name: "fox", IdleA: "🦊", IdleB: "🦊", Sleeping: "😴", Waking: "🦊 ?", Grooming: "🦊 ✨", Curious: "🦊 ?", HappyA: "🦊 ♪", HappyB: "🦊 ♥", Jumping: "🦊 ⤴", PeekingR: "🦊›", PeekingL: "‹🦊", PlayingR: "🦊", PlayingL: "🦊", WalkingR: "🦊 →", WalkingL: "← 🦊"},
	{Name: "rabbit", IdleA: "🐰", IdleB: "🐇", Sleeping: "😴", Waking: "🐰 ?", Grooming: "🐰 ✨", Curious: "🐇 ?", HappyA: "🐰 ♪", HappyB: "🐇 ♥", Jumping: "🐰 ⤴", PeekingR: "🐰›", PeekingL: "‹🐰", PlayingR: "🐰", PlayingL: "🐰", WalkingR: "🐰 →", WalkingL: "← 🐰"},
	{Name: "dog", IdleA: "🐶", IdleB: "🐕", Sleeping: "😴", Waking: "🐶 ?", Grooming: "🐶 ✨", Curious: "🐕 ?", HappyA: "🐶 ♪", HappyB: "🐕 ♥", Jumping: "🐶 ⤴", PeekingR: "🐶›", PeekingL: "‹🐶", PlayingR: "🐶", PlayingL: "🐶", WalkingR: "🐶 →", WalkingL: "← 🐶"},
	{Name: "panda", IdleA: "🐼", IdleB: "🐼", Sleeping: "😴", Waking: "🐼 ?", Grooming: "🐼 ✨", Curious: "🐼 ?", HappyA: "🐼 ♪", HappyB: "🐼 ♥", Jumping: "🐼 ⤴", PeekingR: "🐼›", PeekingL: "‹🐼", PlayingR: "🐼", PlayingL: "🐼", WalkingR: "🐼 →", WalkingL: "← 🐼"},
	{Name: "bear", IdleA: "🐻", IdleB: "🐻", Sleeping: "😴", Waking: "🐻 ?", Grooming: "🐻 ✨", Curious: "🐻 ?", HappyA: "🐻 ♪", HappyB: "🐻 ♥", Jumping: "🐻 ⤴", PeekingR: "🐻›", PeekingL: "‹🐻", PlayingR: "🐻", PlayingL: "🐻", WalkingR: "🐻 →", WalkingL: "← 🐻"},
	{Name: "koala", IdleA: "🐨", IdleB: "🐨", Sleeping: "😴", Waking: "🐨 ?", Grooming: "🐨 ✨", Curious: "🐨 ?", HappyA: "🐨 ♪", HappyB: "🐨 ♥", Jumping: "🐨 ⤴", PeekingR: "🐨›", PeekingL: "‹🐨", PlayingR: "🐨", PlayingL: "🐨", WalkingR: "🐨 →", WalkingL: "← 🐨"},
	{Name: "tiger", IdleA: "🐯", IdleB: "🐅", Sleeping: "😴", Waking: "🐯 ?", Grooming: "🐯 ✨", Curious: "🐅 ?", HappyA: "🐯 ♪", HappyB: "🐅 ♥", Jumping: "🐯 ⤴", PeekingR: "🐯›", PeekingL: "‹🐯", PlayingR: "🐯", PlayingL: "🐯", WalkingR: "🐯 →", WalkingL: "← 🐯"},
	{Name: "lion", IdleA: "🦁", IdleB: "🦁", Sleeping: "😴", Waking: "🦁 ?", Grooming: "🦁 ✨", Curious: "🦁 ?", HappyA: "🦁 ♪", HappyB: "🦁 ♥", Jumping: "🦁 ⤴", PeekingR: "🦁›", PeekingL: "‹🦁", PlayingR: "🦁", PlayingL: "🦁", WalkingR: "🦁 →", WalkingL: "← 🦁"},
	{Name: "monkey", IdleA: "🐵", IdleB: "🐒", Sleeping: "😴", Waking: "🐵 ?", Grooming: "🐵 ✨", Curious: "🐒 ?", HappyA: "🐵 ♪", HappyB: "🐒 ♥", Jumping: "🐵 ⤴", PeekingR: "🐵›", PeekingL: "‹🐵", PlayingR: "🐵", PlayingL: "🐵", WalkingR: "🐵 →", WalkingL: "← 🐵"},
	{Name: "frog", IdleA: "🐸", IdleB: "🐸", Sleeping: "😴", Waking: "🐸 ?", Grooming: "🐸 ✨", Curious: "🐸 ?", HappyA: "🐸 ♪", HappyB: "🐸 ♥", Jumping: "🐸 ⤴", PeekingR: "🐸›", PeekingL: "‹🐸", PlayingR: "🐸", PlayingL: "🐸", WalkingR: "🐸 →", WalkingL: "← 🐸"},
	{Name: "penguin", IdleA: "🐧", IdleB: "🐧", Sleeping: "😴", Waking: "🐧 ?", Grooming: "🐧 ✨", Curious: "🐧 ?", HappyA: "🐧 ♪", HappyB: "🐧 ♥", Jumping: "🐧 ⤴", PeekingR: "🐧›", PeekingL: "‹🐧", PlayingR: "🐧", PlayingL: "🐧", WalkingR: "🐧 →", WalkingL: "← 🐧"},
}

// petState animates a small cat across the footer. Horizontal motion is eased
// with a harmonica spring so the pet accelerates, drifts, and slows naturally
// instead of stepping linearly. While the agent works the pet curls up and
// sleeps; otherwise it wanders between moods.
type petState struct {
	spring    harmonica.Spring
	inited    bool
	pos, vel  float64
	target    float64
	persona   int
	mood      petMood
	moodTicks int
	frame     int
	facing    int // +1 right, -1 left
}

func (p petState) maxX(width int) int {
	m := width - petWidth - 2
	if m < 1 {
		return 1
	}
	return m
}

func (p petState) currentPersona() petPersona {
	if len(petPersonas) == 0 {
		return petPersona{Name: "cat", IdleA: "🐱", IdleB: "😺", Sleeping: "😴"}
	}
	idx := p.persona % len(petPersonas)
	if idx < 0 {
		idx = 0
	}
	return petPersonas[idx]
}

func (p petState) cyclePersona() petState {
	if len(petPersonas) == 0 {
		return p
	}
	p.persona = (p.persona + 1) % len(petPersonas)
	return p
}

// update advances the pet one animation tick.
func (p petState) update(width int, working bool) petState {
	p.frame++
	maxX := p.maxX(width)
	if !p.inited {
		// ~8 fps tick; a low frequency and moderate damping give a relaxed,
		// slightly playful glide.
		p.spring = harmonica.NewSpring(harmonica.FPS(8), 1.7, 0.55)
		p.facing = 1
		p.pos = float64(maxX) / 2
		p.target = p.pos
		p.mood = petSitting
		p.moodTicks = 12
		p.inited = true
	}

	if working {
		if p.mood != petSleeping {
			p.mood = petWaking
			p.moodTicks = 6
		} else {
			p.mood = petSleeping
		}
		p.target = 2 // shuffle to a cozy corner to nap
	} else {
		if p.mood == petSleeping {
			p.mood = petWaking
			p.moodTicks = 8
		}
		p.moodTicks--
		if p.moodTicks <= 0 {
			p = p.pickMood(maxX)
		}
	}

	np, nv := p.spring.Update(p.pos, p.vel, p.target)
	switch {
	case np > p.pos+0.05:
		p.facing = 1
	case np < p.pos-0.05:
		p.facing = -1
	}
	p.pos, p.vel = np, nv
	return p
}

// pickMood selects the next behavior and where to wander for it.
func (p petState) pickMood(maxX int) petState {
	// Weighted toward gentle wandering and idle cuteness.
	bag := []petMood{
		petWalking, petWalking, petWalking,
		petPlaying, petPlaying,
		petPeeking, petCurious, petHappy,
		petSitting, petGrooming, petJumping,
	}
	p.mood = bag[rand.Intn(len(bag))]
	switch p.mood {
	case petWalking:
		p.target = float64(rand.Intn(maxX + 1))
		p.moodTicks = 28 + rand.Intn(34)
	case petPlaying:
		// dart toward a random spot, quick and bouncy
		p.target = float64(rand.Intn(maxX + 1))
		p.moodTicks = 18 + rand.Intn(16)
	case petPeeking:
		if rand.Intn(2) == 0 {
			p.target = 0
		} else {
			p.target = float64(maxX)
		}
		p.moodTicks = 22
	case petJumping:
		p.moodTicks = 10
	default: // sitting, grooming, curious, happy — stay put a beat
		p.moodTicks = 16 + rand.Intn(22)
	}
	return p
}

// render draws the pet at its current position with the mood's face.
func (p petState) render(width int, working bool) string {
	pos := int(p.pos + 0.5)
	if pos < 0 {
		pos = 0
	}
	if m := p.maxX(width); pos > m {
		pos = m
	}
	return stylePet.Render(strings.Repeat(" ", pos) + p.face(working))
}

// face returns the mood-driven emoji cat for the current frame.
func (p petState) face(working bool) string {
	persona := p.currentPersona()
	wiggle := (p.frame/3)%2 == 0
	if working || p.mood == petSleeping {
		z := []string{"z", "z z", "Zz"}[(p.frame/4)%3]
		return persona.Sleeping + " " + z
	}

	right := p.facing >= 0
	switch p.mood {
	case petWaking:
		return persona.Waking
	case petSitting:
		if wiggle {
			return persona.IdleA
		}
		return persona.IdleB
	case petGrooming:
		return persona.Grooming
	case petCurious:
		return persona.Curious
	case petHappy:
		if wiggle {
			return persona.HappyA
		}
		return persona.HappyB
	case petJumping:
		return persona.Jumping
	case petPeeking:
		if right {
			return persona.PeekingR
		}
		return persona.PeekingL
	case petPlaying:
		ball := []string{"●", "○", "◐", "◑"}[p.frame%4]
		if right {
			return persona.PlayingR + " " + ball
		}
		return ball + " " + persona.PlayingL
	default: // walking
		if right {
			return persona.WalkingR
		}
		return persona.WalkingL
	}
}
