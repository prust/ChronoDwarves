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
	player_speed = 4
	window_w     = 1024
	window_h     = 768
	screen_w     = window_w / 4
	screen_h     = window_h / 4
)

var hist_actions = [4]input.Action{action_left, action_right, action_up, action_down}

var red = color.RGBA{R: 255, G: 0, B: 0, A: 255}
var (
	cam               *kamera.Camera
	game_map          *dngn.Layout
	wall_img          *ebiten.Image
	door_img          *ebiten.Image
	floor_img         *ebiten.Image
	is_cam_reset      bool
	show_hitboxes     bool
	tick              int // tick starts at 0, increments 60x/sec, and resets to 0 when you go back in time
	has_time_traveled bool
)

type Game struct {
	player        *Player
	player_anim   [4]*ganim8.Animation // an animation for each of the 4 directions
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
	was_walking := g.player.dx != 0 || g.player.dy != 0
	if g.player_input.ActionIsJustPressed(action_time_travel) {
		tick = 0
		g.player.x = g.player.start_x * 16
		g.player.y = g.player.start_y * 16

		// the "position" in resolv is the *center* of the player, not the top-left
		// so we need to compensate
		g.player.rect.SetPosition(g.player.x+(16/2), g.player.y+(32/2))
		has_time_traveled = true
	}

	var hist_point InputHistoryPoint

	if has_time_traveled {
		// make sure we haven't overrun the list of history points
		if g.player.hist_ix < len(g.player.history) {
			// if the next-up history point is the current tick/frame, advance to it
			next_up_hist_point := g.player.history[g.player.hist_ix]
			if next_up_hist_point.tick == tick {
				hist_point = next_up_hist_point
				g.player.hist_ix++ // advance to next history point
			}
		}
	} else {
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
			g.player.history = append(g.player.history, hist_point)
		}
	}

	// keep is_pressed array updated
	for _, action := range hist_actions {
		if hist_point.just_pressed[action] {
			g.player.is_pressed[action] = true
		} else if hist_point.just_released[action] {
			g.player.is_pressed[action] = false
		}
	}

	if g.player.is_pressed[action_left] {
		g.player.dx = -player_speed
	} else if g.player.is_pressed[action_right] {
		g.player.dx = player_speed
	} else {
		g.player.dx = 0
	}

	if g.player.is_pressed[action_up] {
		g.player.dy = -player_speed
	} else if g.player.is_pressed[action_down] {
		g.player.dy = player_speed
	} else {
		g.player.dy = 0
	}
	is_walking := g.player.dx != 0 || g.player.dy != 0

	g.player.NormalizeVelocity()

	g.player.x += g.player.dx
	g.player.y += g.player.dy
	g.player.rect.Move(g.player.dx, g.player.dy)

	// filter to shapes near the player
	near_shapes := g.player.rect.SelectTouchingCells(4).FilterShapes()
	g.player.rect.IntersectionTest(resolv.IntersectionTestSettings{
		TestAgainst: near_shapes,
		OnIntersect: func(set resolv.IntersectionSet) bool {
			// back off from what we collided/intersected with
			g.player.rect.MoveVec(set.MTV)
			g.player.x += set.MTV.X
			g.player.y += set.MTV.Y
			// keep iterating (in case we're touching something else)
			return true
		},
	})

	if hist_point.just_pressed[action_down] {
		g.player.dir = 0
	} else if hist_point.just_pressed[action_right] {
		g.player.dir = 1
	} else if hist_point.just_pressed[action_left] {
		g.player.dir = 2
	} else if hist_point.just_pressed[action_up] {
		g.player.dir = 3
	} else if hist_point.just_released[action_down] || hist_point.just_released[action_right] || hist_point.just_released[action_left] || hist_point.just_released[action_up] {
		// if the player just released a key, change direction based on any other key that is still pressed
		if g.player.is_pressed[action_down] {
			g.player.dir = 0
		} else if g.player.is_pressed[action_right] {
			g.player.dir = 1
		} else if g.player.is_pressed[action_left] {
			g.player.dir = 2
		} else if g.player.is_pressed[action_up] {
			g.player.dir = 3
		}
	}

	if !was_walking && is_walking {
		g.player.walk_sound.Rewind()
		g.player.walk_sound.Play()
	} else if was_walking && !is_walking {
		g.player.walk_sound.Pause()
		g.player_anim[g.player.dir].GoToFrame(2)
	}

	if is_walking {
		g.player_anim[g.player.dir].Update()
	}

	if is_cam_reset {
		cam.SetTopLeft(0, 0)
	} else {
		cam.LookAt(float64(g.player.x), float64(g.player.y))
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
		if isRectangleOverlap(x1, y1, x2, y2, float64(cell.X*16), float64(cell.Y*16), float64(cell.X*16+16), float64(cell.Y*16+16)) {
			op.GeoM.Reset()
			op.GeoM.Translate(float64(cell.X*16), float64(cell.Y*16))
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
	op.GeoM.Reset()
	op.GeoM.Translate(float64(g.player.x), float64(g.player.y))
	cam.Draw(g.player_anim[g.player.dir].Frame(), op, screen)

	if show_hitboxes {
		for _, wall_rect := range g.wall_rects {
			drawHitbox(wall_rect, cam, screen)
		}
		drawHitbox(g.player.rect, cam, screen)
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
	game_map = dngn.NewLayout(100, 100)
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
	for n := range 100 {
		// left and right walls
		game_map.Set(n, 0, 'x')
		game_map.Set(n, 99, 'x')
		// top and bottom walls
		game_map.Set(0, n, 'x')
		game_map.Set(99, n, 'x')
	}

	// create resolv (collision detection) rectangles for walls in the grid
	// trying a 32x32 "cell" size (for now) for performant collision checks
	g.space = resolv.NewSpace(100*16, 100*16, 32, 32)
	wall_select := game_map.Select().FilterByRune('x')
	g.wall_rects = make([]*resolv.ConvexPolygon, 0, len(wall_select.Cells))
	for cell := range wall_select.Cells {
		wall_rect := resolv.NewRectangleFromTopLeft(float64(cell.X)*16, float64(cell.Y)*16, 16, 16)
		g.wall_rects = append(g.wall_rects, wall_rect)
		g.space.Add(wall_rect)
	}

	// load images/spritesheets
	var character_img = loadImg("dwarf_character.png")
	wall_img = loadImg("wall.png")
	door_img = loadImg("door.png")
	floor_img = loadImg("floor.png")

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
	g.player = &Player{}
	// find a random, empty space in the map to spawn the player
	for _ = range 1000 {
		x := rand.IntN(100)
		y := rand.IntN(100)
		// ensure the cell & the one below (since the player is 2 cells high) are empty
		// disallow the 0,0 coordinate b/c we can't differentiate it from uninitialized vars
		if (x != 0 || y != 0) && game_map.Get(x, y) == ' ' && game_map.Get(x, y) == ' ' {
			g.player.start_x = float64(x)
			g.player.start_y = float64(y)
			break
		}
	}
	if g.player.start_x == 0 && g.player.start_y == 0 {
		panic("Unable to find an empty pair of cells to spawn player after 1000 tries")
	}

	g.player.x = g.player.start_x * 16
	g.player.y = g.player.start_y * 16

	// player hitbox is smaller than the frame
	g.player.rect = resolv.NewRectangleFromTopLeft(g.player.x+2, g.player.y+11, 11, 19)
	g.space.Add(g.player.rect)

	g.audio_context = audio.NewContext(sample_rate)

	walk_wav := loadWav("walk.wav")
	loop_walk := audio.NewInfiniteLoop(walk_wav, walk_wav.Length())
	var err error
	g.player.walk_sound, err = g.audio_context.NewPlayerF32(loop_walk)
	check(err)

	// 16x32 frames, 3 frame columns and 4 frame rows
	g32 := ganim8.NewGrid(16, 32, 16*3, 32*4)
	g.player_anim[0] = ganim8.New(character_img, g32.Frames("1-3", 1), anim_rate)
	g.player_anim[1] = ganim8.New(character_img, g32.Frames("1-3", 2), anim_rate)
	g.player_anim[2] = ganim8.New(character_img, g32.Frames("1-3", 3), anim_rate)
	g.player_anim[3] = ganim8.New(character_img, g32.Frames("1-3", 4), anim_rate)

	cam = kamera.NewCamera(g.player.x, g.player.y, float64(g.screen_w), float64(g.screen_h))
	cam.ShakeEnabled = true
	cam.SmoothType = kamera.SmoothDamp
	cam.SmoothOptions.SmoothDampTimeX = 0.15

	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
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
