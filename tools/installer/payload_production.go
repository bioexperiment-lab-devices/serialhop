//go:build production

package main

import _ "embed"

//go:embed payload/SerialHop.exe
var payload []byte

//go:embed payload/ffmpeg.exe
var ffmpegPayload []byte
