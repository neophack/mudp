//go:build js && wasm

package main

import (
	"syscall/js"

	"mudp/internal/raster"
)

// yuvDecode(format string, w, h int, src Uint8Array) -> Uint8Array RGBA
func yuvDecode(this js.Value, args []js.Value) any {
	format := args[0].String()
	w, h := args[1].Int(), args[2].Int()
	src := copyIn(args[3])
	return toJS(raster.YuvDecode(format, src, w, h))
}

// bayerDecode(pattern string, bitDepth, w, h, rowStart, rowEnd int, src Uint8Array) -> Uint8Array RGBA band
func bayerDecode(this js.Value, args []js.Value) any {
	pattern := args[0].String()
	bitDepth, w, h := args[1].Int(), args[2].Int(), args[3].Int()
	rowStart, rowEnd := args[4].Int(), args[5].Int()
	src := copyIn(args[6])
	return toJS(raster.BayerDecode(src, w, h, bitDepth, pattern, rowStart, rowEnd))
}

// applyIsp(rgba Uint8Array, saturation float64) -> Uint8Array (same pixels, processed)
func applyIsp(this js.Value, args []js.Value) any {
	rgba := copyIn(args[0])
	saturation := args[1].Float()
	raster.ApplyIsp(rgba, saturation)
	return toJS(rgba)
}
