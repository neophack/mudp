// Command rasterwasm compiles to WebAssembly (GOOS=js GOARCH=wasm) and
// exposes mudp/internal/raster's YUV/Bayer decoders and ISP pass to the
// browser as globalThis.mudpRaster.{yuvDecode,bayerDecode,applyIsp}. It is
// loaded by web/lib/wasmRaster.js, both on the main thread and inside the
// RAW-decode worker pool (web/lib/rawDecodeWorker.js), each getting its own
// instance/linear memory.
//
// Built by scripts/build-wasm.sh (or .ps1) into web/wasm/raster.wasm, which
// web/embed.go embeds into the mudp binary — that build step must run before
// `go build`/`go vet`/`go test` on this module, since go:embed reads the file
// from disk at compile time.
//
//go:build js && wasm

package main

import "syscall/js"

func main() {
	ns := js.Global().Get("Object").New()
	ns.Set("yuvDecode", js.FuncOf(yuvDecode))
	ns.Set("bayerDecode", js.FuncOf(bayerDecode))
	ns.Set("applyIsp", js.FuncOf(applyIsp))
	js.Global().Set("mudpRaster", ns)

	// Signal the JS loader (wasmRaster.js) that the exports above are live.
	// go.run() doesn't resolve until this program exits, which (by design,
	// below) never happens, so the loader can't just await it.
	js.Global().Call("__mudpRasterReady")

	select {} // keep the registered js.Func callbacks alive
}

// copyIn pulls a JS Uint8Array's bytes into a new Go []byte.
func copyIn(v js.Value) []byte {
	buf := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(buf, v)
	return buf
}

// toJS copies a Go []byte into a new JS Uint8Array.
func toJS(buf []byte) js.Value {
	out := js.Global().Get("Uint8Array").New(len(buf))
	js.CopyBytesToJS(out, buf)
	return out
}
