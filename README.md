# ChronoDwarves

**Defend your kin against the giants with time-travel: interact with past runs to perform combo attacks.**

But be careful to avoid letting one of your past selves see you or you will rip the fabric of space/time.

The WIP can be played online at https://prust.github.io/ChronoDwarves/.

**Note:** the graphics are placeholders (our pixel artist won't be able to start until Saturday afternoon).

Acknowlegments:

A huge thank-you to [ebitengine](https://ebitengine.org/) and these excellent libraries: [ganim8](https://github.com/yohamta0/ganim8-lib), [resolv](https://github.com/SolarLune/resolv), [dngn](https://github.com/SolarLune/dngn), [ebitengine-input](https://github.com/quasilyte/ebitengine-input), and [kamera](https://github.com/setanarut/kamera).

# How to play locally

In addition to playing the game online at https://prust.github.io/ChronoDwarves/, you can install and run it locally (it uses much less CPU when run locally).

Here are the steps:

- Download & install Go: https://go.dev/dl/
- Install go modules: `go mod tidy`
- Run the game: `go run .`

# How to (re)generate the WASM build (linux/unix):

```
env GOOS=js GOARCH=wasm go build -o ChronoDwarves.wasm github.com/prust/ChronoDwarves
```
