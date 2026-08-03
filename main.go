package main

import (
	"bytes"
	"embed"
	"fmt"
	"image/color"
	"io/fs"
	"log"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/wav"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	input "github.com/quasilyte/ebitengine-input"
	"github.com/setanarut/kamera/v2"
	"github.com/solarlune/dngn"
	et "github.com/solarlune/ebitick"
	"github.com/solarlune/resolv"
	"github.com/yohamta/ganim8/v2"
)

//go:embed sounds
var sounds embed.FS

const (
	// history actions (need to be first)
	action_left input.Action = iota
	action_right
	action_up
	action_down
	action_throw_slime
	action_hit_slime
	// misc non-history actions
	action_cam_reset
	action_hitbox
	action_time_travel
	num_hist_actions = 6
	sample_rate      = 48000
	anim_rate        = time.Second / 8  // 8fps pixel art animation (looping 3-frame walk cycles)
	giant_anim_rate  = time.Second / 24 // probably too many frames in this animation, play it faster
	player_speed     = 3
	throw_speed      = 5
	player_w         = 16
	player_h         = 32
	giant_w          = 48
	giant_h          = 64
	// giant_health_h         = 6
	// giant_health_w         = 36
	giant_damage           = 2
	shockwave_frame        = 9 // the frame at which the giant strikes the ground
	slime_sm_w             = 4
	slime_sm_h             = 4
	slime_med_w            = 10
	slime_med_h            = 10
	grid_size              = 16
	window_w               = 1024
	window_h               = 768
	screen_w               = window_w / 4
	screen_h               = window_h / 4
	map_w                  = 50
	map_h                  = 50
	min_shockwave_delay_ms = 1000
	max_shockwave_delay_ms = 3000
	player_start_health    = 8
	giant_start_health     = 10
	slime_start_health     = 2
	num_slimes_on_map      = 25
)

type Direction int

const (
	down Direction = iota // must be in a new const block to restart
	right
	left
	up
)

var (
	hist_actions         = [num_hist_actions]input.Action{action_left, action_right, action_up, action_down, action_throw_slime, action_hit_slime}
	cam                  *kamera.Camera
	game_map             *dngn.Layout
	character_img        *ebiten.Image
	wall_img             *ebiten.Image
	door_img             *ebiten.Image
	floor_img            *ebiten.Image
	sm_slime_img         *ebiten.Image
	lg_slime_img         *ebiten.Image
	lg_slime_damage_img  *ebiten.Image
	med_slime_img        *ebiten.Image
	med_slime_damage_img *ebiten.Image
	ammo_counter_img     *ebiten.Image
	ammo_counter_w       float64 = 32
	is_cam_reset         bool
	show_hitboxes        bool
	tick                 int // tick starts at 0, increments 60x/sec, and resets to 0 when you go back in time
	shockwave_snd        *audio.Player
	slime_giant_snd      *audio.Player
	slime_wall_snd       *audio.Player
	ambient_snd          *audio.Player
	ambient_boss_snd     *audio.Player
	collect_snd          *audio.Player
	swing_miss_snd       *audio.Player
	red                  = color.RGBA{R: 255, G: 0, B: 0, A: 100}
	see_thru_grey        = color.RGBA{R: 100, G: 100, B: 100, A: 170}
	see_thru_red         = color.RGBA{R: 255, G: 0, B: 0, A: 50}
	see_thru_black       = color.RGBA{R: 0, G: 0, B: 0, A: 150}
	tag_wall             = resolv.NewTag("wall")
	tag_giant            = resolv.NewTag("giant")
	tag_collectible      = resolv.NewTag("collectible")
	tag_live_slime       = resolv.NewTag("live_slime")
	shockwave_dist       = float64(grid_size * 7)
	throw_dist           = float64(grid_size * 4)
	max_los_dist         = float64(grid_size * 8)
	max_hit_dist         = float64(30)
)

type Game struct {
	selves            []*Player // past selves & current self
	player_death_anim *ganim8.Animation
	giant             *Giant
	giant_anim        *ganim8.Animation // one animation for the giant's shockwave punch
	ammo_anim         *ganim8.Animation
	screen_w          int
	screen_h          int
	input_system      input.System
	player_input      *input.Handler
	audio_context     *audio.Context
	space             *resolv.Space
	wall_rects        []*resolv.ConvexPolygon
	slimes            []*Slime
	timer_system      *et.TimerSystem
	shock_circle      bool
	rng_seed1         uint64
	rng_seed2         uint64
	rng               *rand.Rand
	giants_room       *dngn.BSPRoom
	giants_rm_rect    *resolv.ConvexPolygon
}

// set volume based on nearness of the player, max_dist cells away = 0%, 0 cells away = 100%
func (g *Game) VolumeByPlayerDist(rect *resolv.ConvexPolygon, max_dist int, max_vol float64) float64 {
	max_dist_px := float64(max_dist * grid_size)

	curr_self := g.selves[len(g.selves)-1]
	dist := distance(curr_self.x, curr_self.y, rect.Position().X, rect.Position().Y)
	if dist > max_dist_px {
		return 0
	} else {
		pct_of_max_dist := dist / max_dist_px
		inverse_pct := 1 - pct_of_max_dist
		return inverse_pct * max_vol
	}
}

// higher level than loadWav(), not to be used for InfiniteLoop sounds
func (g *Game) LoadSoundPlayer(filename string) *audio.Player {
	wav := loadWav(filename)
	snd_player, err := g.audio_context.NewPlayerF32(wav)
	check(err)
	return snd_player
}

func (g *Game) ResetSlimesOnMap() {
	for range num_slimes_on_map {
		x, y := findEmptyCells(1, 1, g.rng)
		g.SpawnNewSlime(float64(x*grid_size), float64(y*grid_size), true)
	}
}

func (g *Game) IsLineOfSight(start resolv.Vector, end resolv.Vector) bool {
	walls := g.space.FilterShapes().ByTags(tag_wall)
	line_test_settings := resolv.LineTestSettings{Start: start, End: end, TestAgainst: walls, OnIntersect: onLineIntersectDiscontinue}
	// there *is* a clear line-of-sight if there are *not* any intersections w/ walls
	return !resolv.LineTest(line_test_settings)
}

// finds an acceptable starting position near these coordinates
// but WITHOUT line-of-sight to ANY past selves
func (g *Game) findStartPosNear(start_x int, start_y int) (int, int) {
	// work in concentric "circles" (square perimiters, really)
	// starting 2 cells away from the player
	for dist := 2; dist < 50; dist++ {
		for x := start_x - dist; x <= start_x+dist; x++ {
			for y := start_y - dist; y <= start_y+dist; y++ {
				if !inBounds(x, y) {
					continue
				}

				// players take up two cells, so make sure x,y and x,y+1 are both clear of walls
				if game_map.Get(x, y) == ' ' && game_map.Get(x, y+1) == ' ' {

					// make sure no past selves have line-of-sight to the new position
					// adding grid_size/2 gets us to the center of the cell
					vec := resolv.Vector{X: float64(x*grid_size + grid_size/2), Y: float64(y*grid_size + grid_size/2)}
					line_of_sight := false
					for _, self := range g.selves {
						if g.IsLineOfSight(self.rect.Position(), vec) {
							line_of_sight = true
						}
					}
					if !line_of_sight {
						return x, y
					}
				}
			}
		}
	}
	panic("unable to find start position near " + string(start_x) + "," + string(start_y))
}

// each "past self" of a player is a separate Player instance
// with a separate starting position, input history, current position, etc
type Player struct {
	start_x        int // start pos in cell coordinates (not in px)
	start_y        int
	x              float64 // curr pos in px
	y              float64
	dx             float64 // delta position (velocity)
	dy             float64
	rect           *resolv.ConvexPolygon // DRY violation w/ x,y -- should we solely use the collision lib rect?
	dir            Direction             // direction player is facing (indexes the animation array)
	walk_snd       *audio.Player
	hurt_snd       *audio.Player
	death_snd      *audio.Player
	history        []InputHistoryPoint    // condensed array of input history
	hist_ix        int                    // index of the next input history point during a replay
	is_pressed     [num_hist_actions]bool // track state of currently-pressed actions
	health         int
	walk_anim      [4]*ganim8.Animation // an animation for each of the 4 directions
	throw_cooldown bool
	num_slimes     int
	red_fov        bool
	hit_circle     bool
}

func (g *Game) SpawnNewSlime(x, y float64, is_alive bool) *Slime {
	slime := &Slime{x: x, y: y, start_x: x, start_y: y}
	if is_alive {
		slime.rect = resolv.NewRectangleFromTopLeft(slime.x, slime.y, slime_med_w, slime_med_h)
	} else {
		slime.rect = resolv.NewRectangleFromTopLeft(slime.x, slime.y, slime_sm_w, slime_sm_h)
	}
	g.space.Add(slime.rect)
	g.slimes = append(g.slimes, slime)
	if is_alive {
		slime.health = slime_start_health
		slime.rect.Tags().Set(tag_live_slime)
		ms := g.rng.IntN(200)
		g.timer_system.AfterDuration((300+time.Duration(ms))*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
			// always generate these random numbers, regardless of outside happenings, to keep RNG in sync
			rnd_x, rnd_y := g.rng.IntN(3), g.rng.IntN(3)
			if slime.health > 0 {
				if slime.dx > 0 || slime.dy > 0 {
					slime.dx = 0
					slime.dy = 0
				} else {
					// restrict slimes to just horiz or just vert movement
					if g.rng.IntN(2) == 0 {
						slime.dx = float64(rnd_x - 1)
						slime.dy = 0
					} else {
						slime.dx = 0
						slime.dy = float64(rnd_y - 1)
					}
				}
			}
			return et.FinishLoop
		})
	} else {
		slime.rect.Tags().Set(tag_collectible)
	}
	return slime
}

func (self *Player) TakeDamage(damage int, g *Game) {
	is_curr_self := self == g.selves[len(g.selves)-1]
	self.health -= damage
	var sound *audio.Player
	if self.health <= 0 {
		sound = self.death_snd
		self.dx = 0
		self.dy = 0
		for range self.num_slimes + 1 {
			x := self.rect.Center().X + rand.Float64()*20 - 10
			y := self.rect.Center().Y + rand.Float64()*20 - 10
			g.SpawnNewSlime(x, y, false)
		}

		if is_curr_self {
			self.history = append(self.history, InputHistoryPoint{tick: tick, curr_health: 0, num_slimes: self.num_slimes})
		}
	} else {
		sound = self.hurt_snd
	}
	if is_curr_self {
		sound.Rewind()
		sound.Play()
	}
}

// get the 2 points at the edge of the player's field of view (FOV)
func (self *Player) GetFOVPoints() (float64, float64, float64, float64) {
	x, y := self.rect.Position().X, self.rect.Position().Y
	rt_x, dwn_y := normalizeVector(1, 1, max_los_dist)
	var x1, y1, x2, y2 float64
	if self.dir == down || self.dir == right {
		x1, y1 = x+rt_x, y+dwn_y // down-right
	} else { // up || left
		x1, y1 = x-rt_x, y-dwn_y // up-left
	}
	if self.dir == down || self.dir == left {
		x2, y2 = x-rt_x, y+dwn_y // down-left
	} else { // up || right
		x2, y2 = x+rt_x, y-dwn_y // up-right
	}

	// return the points in clockwise order (requires flipping them for left/right)
	// this is important for NewConvexPolygon (downstream)
	if self.dir == down || self.dir == up {
		return x1, y1, x2, y2
	} else {
		return x2, y2, x1, y1
	}
}

// the game's tick increments 60x/sec
// but a history *point* is only recorded for a tick if the input changed
type InputHistoryPoint struct {
	tick          int
	just_pressed  [num_hist_actions]bool
	just_released [num_hist_actions]bool
	mouse_x       float64
	mouse_y       float64
	curr_health   int
	num_slimes    int
}

type Giant struct {
	health          int
	x               float64
	y               float64
	rect            *resolv.ConvexPolygon
	shockwave_punch bool
	shockwave_timer *et.Timer
	red_shockwave   bool
}

type Slime struct {
	x              float64
	y              float64
	dx             float64
	dy             float64
	rect           *resolv.ConvexPolygon
	start_x        float64
	start_y        float64
	health         int
	is_collectible bool
	dealt_dmg      bool
}

type Line struct {
	x1 float64
	y1 float64
	x2 float64
	y2 float64
}

func (g *Game) Update() error {
	g.timer_system.Update()
	g.input_system.Update()

	// diagnostic actions & previous state
	is_cam_reset = g.player_input.ActionIsPressed(action_cam_reset)
	show_hitboxes = g.player_input.ActionIsPressed(action_hitbox)

	if g.player_input.ActionIsJustPressed(action_time_travel) {
		// reset past selves
		for _, self := range g.selves {
			self.health = player_start_health
			new_x := float64(self.start_x * grid_size)
			new_y := float64(self.start_y * grid_size)

			// we can't simply call SetPosition(center_x, center_y) adjusted with player.w/2 & player.h/2
			// because our player hitbox isn't exactly the same shape as the player image (player.w/player.h)
			self.rect.Move(new_x-self.x, new_y-self.y)
			self.x, self.y = new_x, new_y

			// drop past-selves volume, so your current self's volume is most prominent
			self.walk_snd.SetVolume(0.15)
			self.hurt_snd.SetVolume(0.15)
			self.death_snd.SetVolume(0.15)

			self.hist_ix = 0
			self.is_pressed = [num_hist_actions]bool{false, false, false, false, false}
			self.num_slimes = 0
		}

		tick = 0
		g.giant.health = giant_start_health
		g.slimes = g.slimes[:0]
		g.selves = append(g.selves, initPlayer(g))

		// reset the game's random number generator
		source := rand.NewPCG(g.rng_seed1, g.rng_seed2)
		g.rng = rand.New(source)
		g.ResetSlimesOnMap()
	}

	for ix, self := range g.selves {
		// a "past" self (replaying input history) vs "current" self (reacting to player input)
		is_current_self := ix == len(g.selves)-1
		is_past_self := !is_current_self

		was_walking := self.dx != 0 || self.dy != 0
		var hist_point InputHistoryPoint

		if is_past_self {
			// make sure we haven't overrun the list of history points
			if self.hist_ix < len(self.history) {
				// if the next-up history point is the current tick/frame, advance to it
				next_up_hist_point := self.history[self.hist_ix]
				if next_up_hist_point.tick == tick {
					hist_point = next_up_hist_point
					self.hist_ix++ // advance to next history point
					// fudgy cheat: force-update the num_slimes
					self.num_slimes = hist_point.num_slimes
					// make sure we trigger the death logic
					if hist_point.curr_health == 0 && self.health > 0 {
						self.TakeDamage(self.health-hist_point.curr_health, g)
					} else {
						self.health = hist_point.curr_health // fudgy cheat: force-update the health in case something got off
					}
				}
			}

			if !self.red_fov {
				curr_self := g.selves[len(g.selves)-1]
				if curr_self.health > 0 && self.health > 0 {
					past_self_pos := self.rect.Center()
					curr_self_pos := curr_self.rect.Center()
					dist := distance(past_self_pos.X, past_self_pos.Y, curr_self_pos.X, curr_self_pos.Y)
					if dist < max_los_dist {
						// check line-of-sight (to make sure there's not a wall is in the way)
						if g.IsLineOfSight(past_self_pos, curr_self_pos) {
							// and check field-of-view overlap (to check the viewing angle)
							x, y := past_self_pos.X, past_self_pos.Y
							x1, y1, x2, y2 := self.GetFOVPoints()
							fov := resolv.NewConvexPolygon(0, 0, []float64{x, y, x1, y1, x2, y2})

							// resolv doesn't consider it an intersection if one shape is wholly contained by another
							// so we have to do two checks here
							if fov.IsIntersecting(curr_self.rect) || curr_self.rect.IsContainedBy(fov) {

								// make sure the curr_self isn't in the giants' lair (no past-self-damage there)
								if !curr_self.rect.IsContainedBy(g.giants_rm_rect) {
									curr_self.TakeDamage(1, g)
									self.red_fov = true
									g.timer_system.AfterDuration(500*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
										self.red_fov = false
										return et.FinishEnd
									})
								}
							}
						}
					}
				}
			}
		} else {
			// "current" self
			// store just pressed/released action in an input history point
			// *if* the player is still alive
			hist_point = InputHistoryPoint{tick: tick, curr_health: self.health, num_slimes: self.num_slimes}
			if self.health > 0 {
				input_changed := false
				for _, action := range hist_actions {
					if g.player_input.ActionIsJustPressed(action) {
						hist_point.just_pressed[action] = true
						input_changed = true
					}
					if g.player_input.ActionIsJustReleased(action) {
						hist_point.just_released[action] = true
						input_changed = true
					}
				}
				if hist_point.just_pressed[action_throw_slime] {
					info, _ := g.player_input.JustPressedActionInfo(action_throw_slime)
					hist_point.mouse_x, hist_point.mouse_y = cam.ScreenToWorld(int(info.Pos.X), int(info.Pos.Y))
				}
				if input_changed {
					self.history = append(self.history, hist_point)
				}
			}
		}

		// keep is_pressed array updated
		for _, action := range hist_actions {
			if hist_point.just_pressed[action] {
				self.is_pressed[action] = true
			} else if hist_point.just_released[action] {
				self.is_pressed[action] = false
			}
		}

		if self.is_pressed[action_left] {
			self.dx = -player_speed
		} else if self.is_pressed[action_right] {
			self.dx = player_speed
		} else {
			self.dx = 0
		}

		if self.is_pressed[action_up] {
			self.dy = -player_speed
		} else if self.is_pressed[action_down] {
			self.dy = player_speed
		} else {
			self.dy = 0
		}
		is_walking := self.dx != 0 || self.dy != 0

		self.dx, self.dy = normalizeVector(self.dx, self.dy, player_speed)

		self.x += self.dx
		self.y += self.dy
		self.rect.Move(self.dx, self.dy)

		// filter to shapes near the player
		near_shapes := self.rect.SelectTouchingCells(4).FilterShapes()
		self.rect.IntersectionTest(resolv.IntersectionTestSettings{
			TestAgainst: near_shapes.ByTags(tag_wall | tag_live_slime),
			OnIntersect: func(set resolv.IntersectionSet) bool {
				if set.OtherShape.Tags().Has(tag_wall) {
					// back off from the wall we collided/intersected with
					self.rect.MoveVec(set.MTV)
					self.x += set.MTV.X
					self.y += set.MTV.Y
				} else { // live slime
					slime_ix := slices.IndexFunc(g.slimes, func(slime *Slime) bool {
						return slime.rect == set.OtherShape
					})
					if slime_ix > -1 {
						slime := g.slimes[slime_ix]
						if !slime.dealt_dmg && self.health > 0 {
							self.TakeDamage(1, g)
							slime.dealt_dmg = true
							g.timer_system.AfterDuration(500*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
								slime.dealt_dmg = false
								return et.FinishEnd
							})
						}
					}
				}
				// keep iterating (in case we're touching something else)
				return true
			},
		})

		// pick up collectibles
		self.rect.IntersectionTest(resolv.IntersectionTestSettings{
			TestAgainst: near_shapes.ByTags(tag_collectible),
			OnIntersect: func(set resolv.IntersectionSet) bool {
				ix := slices.IndexFunc(g.slimes, func(s *Slime) bool {
					return s.rect == set.OtherShape
				})
				if ix > -1 && self.health > 0 {
					g.slimes = slices.Delete(g.slimes, ix, ix+1)
					g.space.Remove(set.OtherShape)
					self.num_slimes++
					collect_snd.Rewind()
					collect_snd.Play()
				}
				// keep iterating (in case we're touching multiple collectibles)
				return true
			},
		})

		if hist_point.just_pressed[action_down] {
			self.dir = down
		} else if hist_point.just_pressed[action_right] {
			self.dir = right
		} else if hist_point.just_pressed[action_left] {
			self.dir = left
		} else if hist_point.just_pressed[action_up] {
			self.dir = up
		} else if hist_point.just_released[action_down] || hist_point.just_released[action_right] || hist_point.just_released[action_left] || hist_point.just_released[action_up] {
			// if the player just released a key, change direction based on any other key that is still pressed
			if self.is_pressed[action_down] {
				self.dir = down
			} else if self.is_pressed[action_right] {
				self.dir = right
			} else if self.is_pressed[action_left] {
				self.dir = left
			} else if self.is_pressed[action_up] {
				self.dir = up
			}
		}

		if !was_walking && is_walking {
			self.walk_snd.Rewind()
			self.walk_snd.Play()
		} else if was_walking && !is_walking {
			self.walk_snd.Pause()
			self.walk_anim[self.dir].GoToFrame(2)
		}

		if hist_point.just_pressed[action_hit_slime] {
			self.hit_circle = true
			g.timer_system.AfterDuration(200*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
				self.hit_circle = false
				return et.FinishEnd
			})
			did_hit_slime := false
			for _, slime := range g.slimes {
				if slime.health > 0 {
					if slime.rect.DistanceTo(self.rect) <= max_hit_dist {
						slime.health--
						did_hit_slime = true
						if slime.health <= 0 {
							slime.is_collectible = true
							slime.rect.Tags().Set(tag_collectible)
							slime.rect.Tags().Unset(tag_live_slime)

							// shift the slime's top-left position slightly
							// to keep it centered, since it's shrinking
							slime.x += 2
							slime.y += 2
							slime.dx = 0
							slime.dy = 0
						}
						// TODO: play slime-hitting sound
					}
				}
			}
			if did_hit_slime {
				slime_giant_snd.Rewind() // TODO: rename; this is used for hitting slimes, too
				slime_giant_snd.Play()
			} else {
				swing_miss_snd.Rewind()
				swing_miss_snd.Play()
			}
		}

		if hist_point.just_pressed[action_throw_slime] {
			if !self.throw_cooldown && self.num_slimes > 0 {
				self.num_slimes--
				slime := g.SpawnNewSlime(self.x+player_w/2, self.y+player_h/2, false)
				throw_vec_x := hist_point.mouse_x - slime.x
				throw_vec_y := hist_point.mouse_y - slime.y
				slime.dx, slime.dy = normalizeVector(throw_vec_x, throw_vec_y, throw_speed)
				self.throw_cooldown = true
				g.timer_system.AfterDuration(500*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
					self.throw_cooldown = false
					return et.FinishEnd
				})
			}
		}

		if is_walking {
			self.walk_anim[self.dir].Update()
		}

		if is_current_self {
			if is_cam_reset {
				cam.SetTopLeft(0, 0)
			} else {
				cam.LookAt(float64(self.x), float64(self.y))
			}
		}
	}

	curr_self := g.selves[len(g.selves)-1]
	var boss_volume float64
	if curr_self.rect.IsContainedBy(g.giants_rm_rect) {
		boss_volume = 1
	} else {
		boss_volume = 0
	}
	if ambient_boss_snd.Volume() != boss_volume {
		ambient_boss_snd.SetVolume(boss_volume)
	}

	// iterate backwards so we can safely remove them w/out messing up iteration
	for i, slime := range slices.Backward(g.slimes) {
		// skip slimes lying on the ground
		if slime.dx != 0 || slime.dy != 0 {
			slime.x += slime.dx
			slime.y += slime.dy
			slime.rect.Move(slime.dx, slime.dy)
			near_shapes := slime.rect.SelectTouchingCells(4).FilterShapes()
			hit_wall := false
			hit_giant := false
			slime.rect.IntersectionTest(resolv.IntersectionTestSettings{
				TestAgainst: near_shapes.ByTags(tag_wall | tag_giant),
				OnIntersect: func(set resolv.IntersectionSet) bool {
					if set.OtherShape.Tags().Has(tag_giant) && slime.health <= 0 {
						hit_giant = true
						slime_giant_snd.Rewind()
						slime_giant_snd.Play()
						g.giant.health--
						if g.giant.health == 0 {
							g.space.Remove(g.giant.rect)
						}
					} else if set.OtherShape.Tags().Has(tag_wall) {
						hit_wall = true
						// dead slime being thrown at wall (vs live slime bumping wall)
						if slime.health == 0 {
							slime_wall_snd.Rewind()
							slime_wall_snd.Play()
						} else {
							// back off from the wall
							slime.rect.MoveVec(set.MTV)
							slime.x += set.MTV.X
							slime.y += set.MTV.Y
						}
					}
					return true
				},
			})
			dist := distance(slime.start_x, slime.start_y, slime.x, slime.y)
			// applies to dead thrown-slimes
			if slime.health == 0 && (hit_wall || hit_giant || dist > throw_dist) {
				g.slimes = slices.Delete(g.slimes, i, i+1)
				g.space.Remove(slime.rect)
			}
		}
	}

	if g.giant.health > 0 {
		if g.giant.shockwave_timer == nil {
			delay := randomMS(min_shockwave_delay_ms, max_shockwave_delay_ms, g.rng)
			g.giant.shockwave_timer = g.timer_system.AfterDuration(delay, func(_ *et.Timer, _ int) et.FinishMode {
				g.giant.shockwave_punch = true
				g.giant_anim.Resume()
				g.giant.shockwave_timer = nil
				return et.FinishEnd
			})
		}

		g.giant_anim.Update()
		if g.giant.shockwave_punch && g.giant_anim.Position() == shockwave_frame {
			vol_pct := g.VolumeByPlayerDist(g.giant.rect, map_h, 0.5)
			shockwave_snd.SetVolume(vol_pct)
			shockwave_snd.Rewind()
			shockwave_snd.Play()

			cam.AddTrauma(0.5)
			g.shock_circle = true
			g.timer_system.AfterDuration(300*time.Millisecond, func(_ *et.Timer, _ int) et.FinishMode {
				g.shock_circle = false
				g.giant.red_shockwave = false
				return et.FinishEnd
			})

			for ix, self := range g.selves {
				is_current_self := ix == len(g.selves)-1
				if self.health > 0 {
					dist := g.giant.rect.DistanceTo(self.rect)
					if dist < shockwave_dist {
						if g.IsLineOfSight(self.rect.Center(), g.giant.rect.Center()) {
							self.TakeDamage(giant_damage, g)
							if is_current_self {
								g.giant.red_shockwave = true
							}

						}
					}
				}
			}
			g.giant.shockwave_punch = false
		}
	}

	tick++
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Clear()

	// get the camera bounds in world coords for culling purposes
	x1, y1 := cam.ScreenToWorld(0, 0)
	x2, y2 := cam.ScreenToWorld(g.screen_w, g.screen_h)

	// draw the map
	op := &ebiten.DrawImageOptions{}
	for x := range map_w {
		for y := range map_h {
			// cull (only draw what's actually on-screen to avoid 100% CPU usage)
			if isRectangleOverlap(x1, y1, x2, y2, float64(x*grid_size), float64(y*grid_size), float64(x*grid_size+grid_size), float64(y*grid_size+grid_size)) {
				op.GeoM.Reset()
				op.GeoM.Translate(float64(x*grid_size), float64(y*grid_size))
				// smooth anti-aliasing (and so ebitengine batches calls due to identical Filter param)
				// op.Filter = ebiten.FilterLinear

				v := game_map.Get(x, y)
				if v == ' ' {
					cam.Draw(floor_img, op, screen)
				} else if v == '#' {
					cam.Draw(door_img, op, screen)
				}
			}
		}
	}

	// draw the walls on a second pass, so you can see the front of them
	for x := range map_w {
		for y := range map_h {
			// cull (only draw what's actually on-screen to avoid 100% CPU usage)
			if isRectangleOverlap(x1, y1, x2, y2, float64(x*grid_size), float64(y*grid_size), float64(x*grid_size+grid_size), float64(y*grid_size+grid_size)) {
				v := game_map.Get(x, y)
				if v == 'x' {
					op.GeoM.Reset()
					op.GeoM.Translate(float64(x*grid_size), float64(y*grid_size))
					// smooth anti-aliasing (and so ebitengine batches calls due to identical Filter param)
					// op.Filter = ebiten.FilterLinear
					cam.Draw(wall_img, op, screen)
				}
			}
		}
	}

	if show_hitboxes {
		for _, wall_rect := range g.wall_rects {
			drawHitbox(wall_rect, cam, screen)
		}
		drawHitbox(g.giant.rect, cam, screen)
		for _, slime := range g.slimes {
			drawHitbox(slime.rect, cam, screen)
		}
	}

	if g.giant.health > 0 {
		op.GeoM.Reset()
		op.GeoM.Translate(g.giant.x, g.giant.y)
		cam.Draw(g.giant_anim.Frame(), op, screen)
		x, y := cam.ApplyCameraTransformToPoint(g.giant.rect.Position().X, g.giant.rect.Position().Y)
		if g.shock_circle {
			if g.giant.red_shockwave {
				vector.FillCircle(screen, float32(x), float32(y), float32(shockwave_dist), see_thru_red, false)
			} else {
				vector.FillCircle(screen, float32(x), float32(y), float32(shockwave_dist), see_thru_grey, false)
			}
		}
	}

	curr_player := g.selves[len(g.selves)-1]
	if curr_player.hit_circle {
		x, y := cam.ApplyCameraTransformToPoint(curr_player.rect.Position().X, curr_player.rect.Position().Y)
		vector.FillCircle(screen, float32(x), float32(y), float32(max_hit_dist), see_thru_grey, false)
	}

	for ix, player := range g.selves {
		is_past_self := ix < len(g.selves)-1
		if is_past_self && player.health > 0 {
			if !player.rect.IsContainedBy(g.giants_rm_rect) {
				x1, y1, x2, y2 := player.GetFOVPoints()

				x_cam, y_cam := cam.ApplyCameraTransformToPoint(player.rect.Position().X, player.rect.Position().Y)
				x1_cam, y1_cam := cam.ApplyCameraTransformToPoint(x1, y1)
				x2_cam, y2_cam := cam.ApplyCameraTransformToPoint(x2, y2)

				var path vector.Path
				path.MoveTo(float32(x_cam), float32(y_cam))
				path.LineTo(float32(x1_cam), float32(y1_cam))
				path.LineTo(float32(x2_cam), float32(y2_cam))
				path.Close()

				ops := &vector.DrawPathOptions{}
				if player.red_fov {
					ops.ColorScale.ScaleWithColor(see_thru_red)
				} else {
					ops.ColorScale.ScaleWithColor(see_thru_grey)
				}
				vector.FillPath(screen, &path, nil, ops)
			}
		}

		var anim *ganim8.Animation
		if player.health > 0 {
			anim = player.walk_anim[player.dir]
		} else {
			anim = g.player_death_anim
		}
		op.GeoM.Reset()
		op.GeoM.Translate(player.x, player.y)
		cam.Draw(anim.Frame(), op, screen)

		if show_hitboxes {
			drawHitbox(player.rect, cam, screen)
		}
	}

	for _, slime := range g.slimes {
		op.GeoM.Reset()
		op.GeoM.Translate(slime.x, slime.y)
		if slime.health > 1 {
			if slime.dealt_dmg {
				cam.Draw(lg_slime_damage_img, op, screen)
			} else {
				cam.Draw(lg_slime_img, op, screen)
			}
		} else if slime.health > 0 {
			if slime.dealt_dmg {
				cam.Draw(med_slime_damage_img, op, screen)
			} else {
				cam.Draw(med_slime_img, op, screen)
			}
		} else {
			cam.Draw(sm_slime_img, op, screen)
		}
	}

	rm_x1, rm_y1 := cam.ApplyCameraTransformToPoint(float64(g.giants_room.X*grid_size), float64(g.giants_room.Y*grid_size))
	cell_x2, cell_y2 := g.giants_room.X+g.giants_room.W+1, g.giants_room.Y+g.giants_room.H+1
	rm_x2, rm_y2 := cam.ApplyCameraTransformToPoint(float64(cell_x2*grid_size), float64(cell_y2*grid_size))
	vector.FillRect(screen, float32(rm_x1), float32(rm_y1), float32(rm_x2-rm_x1), float32(rm_y2-rm_y1), see_thru_black, false)

	// HUD items (draw last)
	ammo_frame := min(curr_player.num_slimes+1, 11) // frames are one-based
	if g.ammo_anim.Position() != ammo_frame {
		g.ammo_anim.GoToFrame(ammo_frame)
	}
	op.GeoM.Reset()
	op.GeoM.Translate(float64(g.screen_w)-ammo_counter_w-3, 3)
	screen.DrawImage(g.ammo_anim.Frame(), op)
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return g.screen_w, g.screen_h
}

func main() {
	ebiten.SetWindowSize(window_w, window_h)
	ebiten.SetWindowTitle("ChronoDwarves")

	g := &Game{
		screen_w:     screen_w,
		screen_h:     screen_h,
		timer_system: et.NewTimerSystem(),
		rng_seed1:    rand.Uint64(),
		rng_seed2:    rand.Uint64(),
	}
	source := rand.NewPCG(g.rng_seed1, g.rng_seed2)
	g.rng = rand.New(source)

	// generate map
	game_map = dngn.NewLayout(map_w, map_h)
	rms := game_map.GenerateBSP(dngn.NewDefaultBSPOptions())

	// line the outer border of the map with walls
	for n := range map_w {
		// top wall
		if game_map.Get(n, 0) == '#' {
			game_map.Set(n, 1, '#') // door to be blown away, move down
		}
		game_map.Set(n, 0, 'x')

		// bottom wall
		if game_map.Get(n, map_h-1) == '#' {
			game_map.Set(n, map_h-2, '#') // door to be blown away, move up
		}
		game_map.Set(n, map_h-1, 'x')
	}
	for n := range map_h {
		// left wall
		if game_map.Get(0, n) == '#' {
			game_map.Set(1, n, '#') // door to be blown away, move right
		}
		game_map.Set(0, n, 'x')
		// right wall
		if game_map.Get(map_w-1, n) == '#' {
			game_map.Set(map_w-2, n, '#') // door to be blown away, move left
		}
		game_map.Set(map_w-1, n, 'x')
	}

	// extend doors so they are 2 tiles high instead of just 1
	door_select := game_map.Select().FilterByRune('#')
	for cell := range door_select.Cells {
		is_horiz_door := game_map.Get(cell.X-1, cell.Y) == ' ' && game_map.Get(cell.X+1, cell.Y) == ' '
		if is_horiz_door {
			has_clearance_above := game_map.Get(cell.X-1, cell.Y-1) == ' ' && game_map.Get(cell.X+1, cell.Y-1) == ' '
			has_clearance_below := game_map.Get(cell.X-1, cell.Y+1) == ' ' && game_map.Get(cell.X+1, cell.Y+1) == ' '
			if has_clearance_above {
				game_map.Set(cell.X, cell.Y-1, '#')
			} else if has_clearance_below {
				game_map.Set(cell.X, cell.Y+1, '#')
			} else {
				fmt.Println("Warning: Unable to extend door to 2 tiles high: " + string(cell.X) + "," + string(cell.Y))
			}
		}
	}

	// create resolv (collision detection) rectangles for walls in the grid
	// trying a 4x "cell" size (double grid width & double grid height) for performant collision checks
	g.space = resolv.NewSpace(map_w*grid_size, map_h*grid_size, grid_size*2, grid_size*2)
	wall_select := game_map.Select().FilterByRune('x')
	g.wall_rects = make([]*resolv.ConvexPolygon, 0, len(wall_select.Cells))
	for cell := range wall_select.Cells {
		wall_rect := resolv.NewRectangleFromTopLeft(float64(cell.X)*grid_size, float64(cell.Y)*grid_size, grid_size, grid_size)
		wall_rect.Tags().Set(tag_wall)
		g.wall_rects = append(g.wall_rects, wall_rect)
		g.space.Add(wall_rect)
	}

	// load images/spritesheets
	character_img = loadImg("dwarf_character.png")
	wall_img = loadImg("WallTall.png")
	door_img = loadImg("door.png")
	floor_img = loadImg("floor.png")
	sm_slime_img = loadImg("sm_slime.png")
	med_slime_img = loadImg("med_slime.png")
	med_slime_damage_img = loadImg("med_slime_damage.png")
	lg_slime_img = loadImg("lg_slime.png")
	lg_slime_damage_img = loadImg("lg_slime_damage.png")

	ammo_counter_img = loadImg("SlimeAmmo.png")
	g_ammo := ganim8.NewGrid(32, 32, 32*3, 32*4)
	g.ammo_anim = ganim8.New(ammo_counter_img, g_ammo.Frames("1-3", "1-4"), giant_anim_rate)

	// initialize input system
	g.input_system.Init(input.SystemConfig{DevicesEnabled: input.AnyDevice})
	keymap := input.Keymap{
		action_left:        {input.KeyLeft, input.KeyA},
		action_right:       {input.KeyRight, input.KeyD},
		action_up:          {input.KeyUp, input.KeyW},
		action_down:        {input.KeyDown, input.KeyS},
		action_cam_reset:   {input.KeyC},
		action_hitbox:      {input.KeyH},
		action_time_travel: {input.KeyT},
		action_throw_slime: {input.KeyMouseLeft},
		action_hit_slime:   {input.KeyMouseRight},
	}

	var err error
	g.audio_context = audio.NewContext(sample_rate)
	shockwave_snd = g.LoadSoundPlayer("shockwave.wav")
	slime_giant_snd = g.LoadSoundPlayer("hit_giant.wav")
	slime_wall_snd = g.LoadSoundPlayer("hit_wall.wav")
	collect_snd = g.LoadSoundPlayer("hit_wall.wav")
	swing_miss_snd = g.LoadSoundPlayer("hit_wall.wav")

	ambient_wav := loadWav("ambient.wav")
	ambient_boss_wav := loadWav("ambient_boss.wav")
	loop_ambient := audio.NewInfiniteLoop(ambient_wav, ambient_wav.Length())
	loop_ambient_boss := audio.NewInfiniteLoop(ambient_boss_wav, ambient_boss_wav.Length())
	ambient_snd, err = g.audio_context.NewPlayerF32(loop_ambient)
	check(err)
	ambient_boss_snd, err = g.audio_context.NewPlayerF32(loop_ambient_boss)
	check(err)
	ambient_snd.Play()
	ambient_boss_snd.Play()
	ambient_boss_snd.SetVolume(0)

	g.player_input = g.input_system.NewHandler(0, keymap)
	player := initPlayer(g)
	g.selves = append(g.selves, player)

	// 3 frame columns and 5 frame rows
	g_pl := ganim8.NewGrid(player_w, player_h, player_w*3, player_h*5)
	g.player_death_anim = ganim8.New(character_img, g_pl.Frames(1, 5), anim_rate)

	giant_img := loadImg("giant.png")
	g_gi := ganim8.NewGrid(giant_w, giant_h, giant_w*15, giant_h*1)
	g.giant_anim = ganim8.New(giant_img, g_gi.Frames("1-15", 1), giant_anim_rate, func(anim *ganim8.Animation, loops int) {
		g.giant_anim.Pause()
		g.giant_anim.GoToFrame(1)
	})
	g.giant_anim.Pause()

	g.giant = &Giant{health: giant_start_health}
	x, y := findEmptyCells(giant_w/grid_size, giant_h/grid_size, nil)
	g.giant.x, g.giant.y = float64(x*grid_size), float64(y*grid_size)
	g.giant.rect = resolv.NewRectangleFromTopLeft(g.giant.x+3, g.giant.y+16, giant_w-4, giant_h-18)
	g.giant.rect.Tags().Set(tag_giant)
	g.space.Add(g.giant.rect)

	// figure out which room is the giants' & store it
	ix := slices.IndexFunc(rms, func(room *dngn.BSPRoom) bool {
		return x > room.X && x < room.X+room.W &&
			y > room.Y && y < room.Y+room.H
	})
	if ix == -1 {
		panic("Giant's room not located: " + string(x) + "," + string(y))
	}
	g.giants_room = rms[ix]
	g.giants_rm_rect = resolv.NewRectangleFromTopLeft(float64(rms[ix].X*grid_size), float64(rms[ix].Y*grid_size), float64((rms[ix].W+1)*grid_size), float64((rms[ix].H+1)*grid_size))

	cam = kamera.NewCamera(player.x, player.y, float64(g.screen_w), float64(g.screen_h))
	camera_shake_options := kamera.DefaultCameraShakeOptions()
	camera_shake_options.Decay = 0.95
	camera_shake_options.Noise.Frequency = 0.2
	camera_shake_options.Noise.Lacunarity = 4.0
	cam.ShakeEnabled = true
	cam.ShakeOptions = camera_shake_options

	cam.SmoothType = kamera.SmoothDamp
	cam.SmoothOptions.SmoothDampTimeX = 0.12
	cam.SmoothOptions.SmoothDampMaxSpeedX = 2500
	cam.SmoothOptions.SmoothDampTimeY = 0.12
	cam.SmoothOptions.SmoothDampMaxSpeedY = 2500

	g.ResetSlimesOnMap()
	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}

func initPlayer(g *Game) *Player {
	player := &Player{health: player_start_health}

	if len(g.selves) > 0 {
		last_self := g.selves[len(g.selves)-1]
		player.start_x, player.start_y = g.findStartPosNear(last_self.start_x, last_self.start_y)
	} else {
		player.start_x, player.start_y = findEmptyCells(player_w/grid_size, player_h/grid_size, nil)
	}
	player.x = float64(player.start_x * grid_size)
	player.y = float64(player.start_y * grid_size)

	// player hitbox is smaller than the frame
	player.rect = resolv.NewRectangleFromTopLeft(player.x+2, player.y+11, 11, 20)
	g.space.Add(player.rect)

	walk_wav := loadWav("walk.wav")
	loop_walk := audio.NewInfiniteLoop(walk_wav, walk_wav.Length())
	var err error
	player.walk_snd, err = g.audio_context.NewPlayerF32(loop_walk)
	check(err)

	player.hurt_snd = g.LoadSoundPlayer("hurt.wav")
	player.death_snd = g.LoadSoundPlayer("death.wav")

	g_pl := ganim8.NewGrid(player_w, player_h, player_w*3, player_h*5)
	player.walk_anim[0] = ganim8.New(character_img, g_pl.Frames("1-3", 1), anim_rate)
	player.walk_anim[1] = ganim8.New(character_img, g_pl.Frames("1-3", 2), anim_rate)
	player.walk_anim[2] = ganim8.New(character_img, g_pl.Frames("1-3", 3), anim_rate)
	player.walk_anim[3] = ganim8.New(character_img, g_pl.Frames("1-3", 4), anim_rate)

	return player
}

func inBounds(x int, y int) bool {
	return x >= 0 && x < map_w && y >= 0 && y < map_h
}

// pass the # of adjacent empty cells needed (1 wide x 2 high for a player)
func findEmptyCells(width int, height int, rng *rand.Rand) (int, int) {
	// find a random, empty space in the map
	for _ = range 1000 {
		var x, y int
		if rng != nil {
			x = rng.IntN(map_w)
			y = rng.IntN(map_h)
		} else {
			x = rand.IntN(map_w)
			y = rand.IntN(map_h)
		}

		// ensure the requested range of cells are all empty
		all_are_empty := true
		for w := range width {
			for h := range height {
				if game_map.Get(x+w, y+h) != ' ' {
					all_are_empty = false
				}
			}
		}
		if all_are_empty {
			return x, y
		}
	}
	panic("Unable to find an empty pair of cells to spawn player after 1000 tries")
}

// wav files shouldn't be closed here b/c audio.Player manages stream state
func loadWav(filename string) *wav.Stream {
	f, err := fs.ReadFile(sounds, "sounds/"+filename)
	check(err)
	reader := bytes.NewReader(f)
	wav_stream, err := wav.DecodeF32(reader)
	check(err)
	return wav_stream
}

func loadImg(filename string) *ebiten.Image {
	wall_img, _, err := ebitenutil.NewImageFromFile("images/" + filename)
	check(err)
	return wall_img
}

func isOnScreen(g *Game, rect *resolv.ConvexPolygon) bool {
	x1, y1 := cam.ScreenToWorld(0, 0)
	x2, y2 := cam.ScreenToWorld(g.screen_w, g.screen_h)
	bnds := rect.Bounds()
	return isRectangleOverlap(x1, y1, x2, y2, bnds.Min.X, bnds.Min.Y, bnds.Max.X, bnds.Max.Y)
}

func isRectangleOverlap(x1 float64, y1 float64, x2 float64, y2 float64, x3 float64, y3 float64, x4 float64, y4 float64) bool {
	// If any of these are true, the rectangles do NOT overlap
	if y3 >= y2 || y4 <= y1 || x3 >= x2 || x4 <= x1 {
		return false
	}
	return true
}

func randomMS(min int, max int, rng *rand.Rand) time.Duration {
	return time.Duration(rng.IntN(max-min)+min) * time.Millisecond
}

func drawHitbox(box *resolv.ConvexPolygon, cam *kamera.Camera, screen *ebiten.Image) {
	x := box.ShapeBase.Position().X
	y := box.ShapeBase.Position().Y
	var prev_vec resolv.Vector
	for ix, vec := range box.Points {
		if ix > 0 {
			drawLine(x+prev_vec.X, y+prev_vec.Y, x+vec.X, y+vec.Y, red, cam, screen)
		}
		prev_vec = vec
	}
	vec := box.Points[0]
	drawLine(x+prev_vec.X, y+prev_vec.Y, x+vec.X, y+vec.Y, red, cam, screen)
}

// func drawRectangle(x float64, y float64, x2 float64, y2 float64, col color.RGBA, cam *kamera.Camera, screen *ebiten.Image) {

// }

func drawLine(x float64, y float64, x2 float64, y2 float64, col color.RGBA, cam *kamera.Camera, screen *ebiten.Image) {
	x, y = cam.ApplyCameraTransformToPoint(x, y)
	x2, y2 = cam.ApplyCameraTransformToPoint(x2, y2)
	vector.StrokeLine(screen, float32(x), float32(y), float32(x2), float32(y2), 1, col, false)
}

// boilerplate function that tells resolve to not continue after finding the first intersection
func onLineIntersectDiscontinue(set resolv.IntersectionSet, index, max int) bool {
	return false
}

func distance(x1 float64, y1 float64, x2 float64, y2 float64) float64 {
	return vectorLength(x2-x1, y2-y1)
}

func normalizeVector(x float64, y float64, desired_len float64) (float64, float64) {
	curr_len := vectorLength(x, y)
	if curr_len == 0 {
		return 0, 0
	}
	return x * desired_len / curr_len, y * desired_len / curr_len
}

func vectorLength(x float64, y float64) float64 {
	return math.Sqrt(x*x + y*y)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
