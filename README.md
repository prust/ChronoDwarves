# ebitengine-template

General template repository that integrates the main libs as a starting point for game jams.

View online at https://prust.github.io/ebitengine-template/.

Libraries:

- [x] [ebitengine](https://ebitengine.org/) (game engine for Go)
- [x] [ganim8](https://github.com/yohamta0/ganim8-lib) (animation lib)
- [x] [resolv](https://github.com/SolarLune/resolv) (collision lib)
- [x] [dngn](https://github.com/SolarLune/dngn) (random map generation lib, supports [BSP generation](https://www.roguebasin.com/index.php?title=Basic_BSP_Dungeon_generation) and [random walk](https://www.roguebasin.com/index.php?title=Random_Walk_Cave_Generation) cave generation)
- [x] [ebitengine-input](https://github.com/quasilyte/ebitengine-input) (input lib)
- [x] [kamera](https://github.com/setanarut/kamera) (camera lib)

# How to install and run the game from the terminal

- Download & install Go: https://go.dev/dl/
- Install go modules: `go mod tidy`
- Run the game: `go run .`

# How to build the game for the browser

```
env GOOS=js GOARCH=wasm go build -o ebitengine-template.wasm github.com/prust/ebitengine-template
```
