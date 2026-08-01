# ChronoDwarves

https://prust.github.io/ChronoDwarves/

**Defend your kin against the giants with time-travel: interact with past runs to perform combo attacks.**

But be careful to avoid letting one of your past selves see you or you will rip the fabric of space/time.

**Note:** the graphics are placeholders (our pixel artist won't be able to start until Saturday afternoon).

# How to play locally

In addition to playing the game [online](https://prust.github.io/ChronoDwarves/), you can install & run it locally (this uses much less CPU than the web version).

Here are the steps:

- Download & install Go: https://go.dev/dl/
- Download the code: `git clone https://github.com/prust/ChronoDwarves.git`
- Change to the directory: `cd ChronoDwarves`
- Install go modules: `go mod tidy`
- Run the game: `go run .`

# How to (re)generate the web (WASM) build (linux/unix):

```
env GOOS=js GOARCH=wasm go build -o ChronoDwarves.wasm github.com/prust/ChronoDwarves
```

# Acknowlegments

A huge thank-you to [ebitengine](https://ebitengine.org/) and these excellent libraries: [ganim8](https://github.com/yohamta0/ganim8-lib), [resolv](https://github.com/SolarLune/resolv), [dngn](https://github.com/SolarLune/dngn), [ebitengine-input](https://github.com/quasilyte/ebitengine-input), and [kamera](https://github.com/setanarut/kamera).
