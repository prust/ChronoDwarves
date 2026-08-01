package main

import (
	"bytes"
	"embed"
	"image/color"
	"io/fs"
	"log"
	"math"
	"math/rand/v2"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/wav"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	input "github.com/quasilyte/ebitengine-input"
	"github.com/setanarut/kamera/v2"
	"github.com/solarlune/dngn"
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
	// misc non-history actions
	action_cam_reset
	action_hitbox
	action_time_travel

	sample_rate  = 48000
	anim_rate    = time.Second / 8 // 8fps pixel art animation (looping 3-frame walk cycles)
	player_speed = 3
	player_w     = 16
	player_h     = 32
	giant_w      = 48
	giant_h      = 64
	grid_size    = 16
	window_w     = 1024
	window_h     = 768
	screen_w     = window_w / 4
	screen_h     = window_h / 4
	map_w        = 50
	map_h        = 50
)

var (
	hist_actions  = [4]input.Action{action_left, action_right, action_up, action_down}
	cam           *kamera.Camera
	game_map      *dngn.Layout
	wall_img      *ebiten.Image
	door_img      *ebiten.Image
	floor_img     *ebiten.Image
	giant_img     *ebiten.Image
	is_cam_reset  bool
	show_hitboxes bool
	tick          int // tick starts at 0, increments 60x/sec, and resets to 0 when you go back in time
	red           = color.RGBA{R: 255, G: 0, B: 0, A: 255}
	tag_wall      = resolv.NewTag("wall")
)

type Game struct {
	selves        []*Player            // past selves & current self
	player_anim   [4]*ganim8.Animation // an animation for each of the 4 directions
	giant         *Giant
	screen_w      int
	screen_h      int
	input_system  input.System
	player_input  *input.Handler
	audio_context *audio.Context
	space         *resolv.Space
	wall_rects    []*resolv.ConvexPolygon
}

// each "past self" of a player is a separate Player instance
// with a separate starting position, input history, current position, etc
type Player struct {
	start_x    float64 // start pos in cell coordinates (not in px)
	start_y    float64
	x          float64 // curr pos in px
	y          float64
	dx         float64 // delta position (velocity)
	dy         float64
	rect       *resolv.ConvexPolygon // DRY violation w/ x,y -- should we solely use the collision lib rect?
	dir        int                   // direction player is facing (indexes the animation array)
	walk_sound *audio.Player
	history    []InputHistoryPoint // condensed array of input history
	hist_ix    int                 // index of the next input history point during a replay
	is_pressed [4]bool             // track state of currently-pressed actions
}

// the game's tick increments 60x/sec
// but a history *point* is only recorded for a tick if the input changed
type InputHistoryPoint struct {
	tick          int
	just_pressed  [4]bool
	just_released [4]bool
}

type Giant struct {
	x float64
	y float64
}

func (p *Player) NormalizeVelocity() {
	length_squared := math.Sqrt(p.dx*p.dx + p.dy*p.dy)
	if length_squared == 0 {
		return
	} else {
		p.dx *= player_speed / length_squared
		p.dy *= player_speed / length_squared
	}
}

func (g *Game) Update() error {
	g.input_system.Update()

	// diagnostic actions & previous state
	is_cam_reset = g.player_input.ActionIsPressed(action_cam_reset)
	show_hitboxes = g.player_input.ActionIsPressed(action_hitbox)

	if g.player_input.ActionIsJustPressed(action_time_travel) {
		tick = 0
		g.selves = append(g.selves, initPlayer(g))
	}

	for ix, self := range g.selves {
		// a "past" self (replaying input history) vs "current" self (reacting to player input)
		is_current_self := ix == len(g.selves)-1
		is_past_self := !is_current_self

		if g.player_input.ActionIsJustPressed(action_time_travel) {
			self.x = self.start_x * grid_size
			self.y = self.start_y * grid_size

			// the "position" in resolv is the *center* of the player, not the top-left
			// so we need to compensate
			self.rect.SetPosition(self.x+(player_w/2), self.y+(player_h/2))
			self.hist_ix = 0
		}

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
				}
			}
		} else {
			// "current" self
			// store just pressed/released action in an input history point
			hist_point = InputHistoryPoint{tick: tick}
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
			if input_changed {
				self.history = append(self.history, hist_point)
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

		self.NormalizeVelocity()

		self.x += self.dx
		self.y += self.dy
		self.rect.Move(self.dx, self.dy)

		// filter to shapes near the player
		near_shapes := self.rect.SelectTouchingCells(4).FilterShapes()
		self.rect.IntersectionTest(resolv.IntersectionTestSettings{
			TestAgainst: near_shapes.ByTags(tag_wall),
			OnIntersect: func(set resolv.IntersectionSet) bool {
				// back off from what we collided/intersected with
				self.rect.MoveVec(set.MTV)
				self.x += set.MTV.X
				self.y += set.MTV.Y
				// keep iterating (in case we're touching something else)
				return true
			},
		})

		if hist_point.just_pressed[action_down] {
			self.dir = 0
		} else if hist_point.just_pressed[action_right] {
			self.dir = 1
		} else if hist_point.just_pressed[action_left] {
			self.dir = 2
		} else if hist_point.just_pressed[action_up] {
			self.dir = 3
		} else if hist_point.just_released[action_down] || hist_point.just_released[action_right] || hist_point.just_released[action_left] || hist_point.just_released[action_up] {
			// if the player just released a key, change direction based on any other key that is still pressed
			if self.is_pressed[action_down] {
				self.dir = 0
			} else if self.is_pressed[action_right] {
				self.dir = 1
			} else if self.is_pressed[action_left] {
				self.dir = 2
			} else if self.is_pressed[action_up] {
				self.dir = 3
			}
		}

		if !was_walking && is_walking {
			self.walk_sound.Rewind()
			self.walk_sound.Play()
		} else if was_walking && !is_walking {
			self.walk_sound.Pause()
			g.player_anim[self.dir].GoToFrame(2)
		}

		if is_walking {
			g.player_anim[self.dir].Update()
		}

		if is_current_self {
			if is_cam_reset {
				cam.SetTopLeft(0, 0)
			} else {
				cam.LookAt(float64(self.x), float64(self.y))
			}
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
	map_select := game_map.Select()
	op := &ebiten.DrawImageOptions{}
	for cell := range map_select.Cells {
		// cull (only draw what's actually on-screen to avoid 100% CPU usage)
		if isRectangleOverlap(x1, y1, x2, y2, float64(cell.X*grid_size), float64(cell.Y*grid_size), float64(cell.X*grid_size+grid_size), float64(cell.Y*grid_size+grid_size)) {
			op.GeoM.Reset()
			op.GeoM.Translate(float64(cell.X*grid_size), float64(cell.Y*grid_size))
			// smooth anti-aliasing (and so ebitengine batches calls due to identical Filter param)
			// op.Filter = ebiten.FilterLinear

			v := game_map.Get(cell.X, cell.Y)
			if v == 'x' {
				cam.Draw(wall_img, op, screen)
			} else if v == ' ' {
				cam.Draw(floor_img, op, screen)
			} else if v == '#' {
				cam.Draw(door_img, op, screen)
			}
		}
	}

	if show_hitboxes {
		for _, wall_rect := range g.wall_rects {
			drawHitbox(wall_rect, cam, screen)
		}
	}

	op.GeoM.Reset()
	op.GeoM.Translate(g.giant.x, g.giant.y)
	cam.Draw(giant_img, op, screen)

	for _, player := range g.selves {
		op.GeoM.Reset()
		op.GeoM.Translate(player.x, player.y)
		cam.Draw(g.player_anim[player.dir].Frame(), op, screen)

		if show_hitboxes {
			drawHitbox(player.rect, cam, screen)
		}
	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return g.screen_w, g.screen_h
}

func main() {
	ebiten.SetWindowSize(window_w, window_h)
	ebiten.SetWindowTitle("Ebitengine Template")

	g := &Game{
		screen_w: screen_w,
		screen_h: screen_h,
	}

	// generate map
	game_map = dngn.NewLayout(map_w, map_h)
	game_map.GenerateBSP(dngn.NewDefaultBSPOptions())
	// extend doors so they are 2 tiles high instead of just 1
	door_select := game_map.Select().FilterByRune('#')
	for cell := range door_select.Cells {
		is_in_vert_wall := game_map.Get(cell.X, cell.Y-1) == 'x' && game_map.Get(cell.X, cell.Y+1) == 'x'
		if is_in_vert_wall {
			game_map.Set(cell.X, cell.Y-1, '#')
		}
	}

	// line the outer border of the map with walls
	for n := range map_w {
		// left and right walls
		game_map.Set(n, 0, 'x')
		game_map.Set(n, map_h-1, 'x')
	}
	for n := range map_h {
		// top and bottom walls
		game_map.Set(0, n, 'x')
		game_map.Set(map_w-1, n, 'x')
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
	var character_img = loadImg("dwarf_character.png")
	wall_img = loadImg("wall.png")
	door_img = loadImg("door.png")
	floor_img = loadImg("floor.png")
	giant_img = loadImg("giant.png")

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
	}
	g.player_input = g.input_system.NewHandler(0, keymap)
	g.audio_context = audio.NewContext(sample_rate)
	player := initPlayer(g)
	g.selves = append(g.selves, player)

	// 3 frame columns and 4 frame rows
	g32 := ganim8.NewGrid(player_w, player_h, player_w*3, player_h*4)
	g.player_anim[0] = ganim8.New(character_img, g32.Frames("1-3", 1), anim_rate)
	g.player_anim[1] = ganim8.New(character_img, g32.Frames("1-3", 2), anim_rate)
	g.player_anim[2] = ganim8.New(character_img, g32.Frames("1-3", 3), anim_rate)
	g.player_anim[3] = ganim8.New(character_img, g32.Frames("1-3", 4), anim_rate)

	g.giant = &Giant{}
	g.giant.x, g.giant.y = findEmptyCells(giant_w/grid_size, giant_h/grid_size)

	cam = kamera.NewCamera(player.x, player.y, float64(g.screen_w), float64(g.screen_h))
	cam.ShakeEnabled = true
	cam.SmoothType = kamera.SmoothDamp
	cam.SmoothOptions.SmoothDampTimeX = 0.12
	cam.SmoothOptions.SmoothDampMaxSpeedX = 2500
	cam.SmoothOptions.SmoothDampTimeY = 0.12
	cam.SmoothOptions.SmoothDampMaxSpeedY = 2500

	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}

func initPlayer(g *Game) *Player {
	player := &Player{}

	player.start_x, player.start_y = findEmptyCells(player_w/grid_size, player_h/grid_size)
	player.x = player.start_x * grid_size
	player.y = player.start_y * grid_size

	// player hitbox is smaller than the frame
	player.rect = resolv.NewRectangleFromTopLeft(player.x+2, player.y+11, 11, 19)
	g.space.Add(player.rect)

	walk_wav := loadWav("walk.wav")
	loop_walk := audio.NewInfiniteLoop(walk_wav, walk_wav.Length())
	var err error
	player.walk_sound, err = g.audio_context.NewPlayerF32(loop_walk)
	check(err)

	return player
}

// pass the # of adjacent empty cells needed (1 wide x 2 high for a player)
func findEmptyCells(width int, height int) (float64, float64) {
	// find a random, empty space in the map
	for _ = range 1000 {
		x := rand.IntN(map_w)
		y := rand.IntN(map_h)

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
			return float64(x), float64(y)
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

func isRectangleOverlap(x1 float64, y1 float64, x2 float64, y2 float64, x3 float64, y3 float64, x4 float64, y4 float64) bool {
	// If any of these are true, the rectangles do NOT overlap
	if y3 >= y2 || y4 <= y1 || x3 >= x2 || x4 <= x1 {
		return false
	}
	return true
}

// drawRedRect() is for debugging purposes (takes world coordinates, translates to screen coords before drawing)
func drawRedRect(x float64, y float64, x2 float64, y2 float64, cam *kamera.Camera, screen *ebiten.Image) {
	drawRedLine(x, y, x2, y, cam, screen)
	drawRedLine(x2, y, x2, y2, cam, screen)
	drawRedLine(x2, y2, x, y2, cam, screen)
	drawRedLine(x, y2, x, y, cam, screen)
}

func drawHitbox(box *resolv.ConvexPolygon, cam *kamera.Camera, screen *ebiten.Image) {
	x := box.ShapeBase.Position().X
	y := box.ShapeBase.Position().Y
	var prev_vec resolv.Vector
	for ix, vec := range box.Points {
		if ix > 0 {
			drawRedLine(x+prev_vec.X, y+prev_vec.Y, x+vec.X, y+vec.Y, cam, screen)
		}
		prev_vec = vec
	}
	vec := box.Points[0]
	drawRedLine(x+prev_vec.X, y+prev_vec.Y, x+vec.X, y+vec.Y, cam, screen)
}

func drawRedLine(x float64, y float64, x2 float64, y2 float64, cam *kamera.Camera, screen *ebiten.Image) {
	x, y = cam.ApplyCameraTransformToPoint(x, y)
	x2, y2 = cam.ApplyCameraTransformToPoint(x2, y2)
	vector.StrokeLine(screen, float32(x), float32(y), float32(x2), float32(y2), 1, red, false)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
